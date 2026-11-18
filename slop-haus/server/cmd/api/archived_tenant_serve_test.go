package main

import (
	"context"
	"github.com/lowaa/slop-factory/go-auth/auth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Every credential a tenant's people and agents hold stops working the
// moment the tenant is archived, at each way in, and works again when it is
// unarchived. The checks are central (authn.ArchivedTenantGuard for
// sessions, the oauth service for every token), so this goes through the
// booted server rather than each service.
func TestServe_AnArchivedTenantsCredentialsAreRefusedAtEveryEntryPoint(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	who, userToken := b.signedInUser(t)
	ctx := db.WithTenant(context.Background(), who.TenantID)
	_, err := b.appDB.Global().Exec(ctx,
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'member')`, uuid.New(), who.TenantID, who.ID)
	require.NoError(t, err)
	conductorID := uuid.New()
	_, err = b.appDB.Exec(ctx, `INSERT INTO agents (id, name, kind, owner_user_id) VALUES ($1, 'conductor', 'conductor', $2)`, conductorID, who.ID)
	require.NoError(t, err)

	clientID, agent := b.agentCredentialsFor(t, who, &oauth.Decision{Approve: true, AgentName: "worker"})
	_, conductor := b.agentCredentialsFor(t, who, &oauth.Decision{Approve: true, AgentID: &conductorID})

	userGet := &request{method: http.MethodGet, url: b.base + "/api/queue/blockage", token: userToken}
	agentClaim := &request{method: http.MethodPost, url: b.base + "/api/claims", body: `{}`, token: agent.AccessToken}
	mcpInit := &request{method: http.MethodPost, url: b.base + "/mcp", token: agent.AccessToken, body: mcpInitialize,
		header: map[string]string{"Accept": "application/json, text/event-stream"}}
	conductorPoll := &request{method: http.MethodPost, url: b.base + "/api/conductor/poll", body: `{"host_id":"` + uuid.NewString() + `","ack":0,"wait_seconds":0}`, token: conductor.AccessToken}
	refresh := &request{method: http.MethodPost, url: b.base + "/oauth/token", header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		body: url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {agent.RefreshToken}}.Encode()}

	// Not the status each wants, which differs by entry point: just that they
	// work now and are refused after.
	okBefore := map[string]*request{"board session": userGet, "agent REST": agentClaim, "MCP": mcpInit, "conductor host": conductorPoll}
	for name, r := range okBefore {
		resp := do(t, r)
		require.Less(t, resp.code, 300, name+" works before the archive: "+resp.body)
	}

	_, err = b.appDB.Global().Exec(ctx, `UPDATE global_tenants SET archived_at = now(), archived_by = $2 WHERE id = $1`, who.TenantID, who.ID)
	require.NoError(t, err)

	resp := do(t, userGet)
	assert.Equal(t, http.StatusForbidden, resp.code, resp.body)
	assert.Contains(t, resp.body, `"code":"tenant_archived"`)
	for name, r := range map[string]*request{"agent REST": agentClaim, "MCP": mcpInit, "conductor host": conductorPoll} {
		resp := do(t, r)
		assert.Equal(t, http.StatusUnauthorized, resp.code, name+": "+resp.body)
	}
	resp = do(t, refresh)
	assert.Equal(t, http.StatusBadRequest, resp.code, resp.body)
	assert.Contains(t, resp.body, "invalid_grant")

	_, err = b.appDB.Global().Exec(ctx, `UPDATE global_tenants SET archived_at = NULL, archived_by = NULL WHERE id = $1`, who.TenantID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, do(t, userGet).code)
	assert.Less(t, do(t, agentClaim).code, 300)
	assert.Equal(t, http.StatusOK, do(t, refresh).code, "the refresh token was not spent by the refusal")
}

// Archiving over the booted API switches the tenant's agents off: the user
// service is handed the agent service in newServices, without which archive
// fails closed with 503.
func TestServe_ArchivingATenantSwitchesItsAgentsOff(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	who, _ := b.signedInUser(t)
	ctx := db.WithTenant(context.Background(), who.TenantID)
	other := dbtest.NewTenant(t, b.appDB)
	for _, tenant := range []uuid.UUID{who.TenantID, other} {
		_, err := b.appDB.Global().Exec(ctx,
			`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'owner') ON CONFLICT DO NOTHING`, uuid.New(), tenant, who.ID)
		require.NoError(t, err)
	}
	var name string
	require.NoError(t, b.appDB.Global().QueryRow(ctx, `SELECT name FROM global_tenants WHERE id = $1`, who.TenantID).Scan(&name))
	token, err := auth.NewSigner([]byte(bootJWTSecret), b.cfg.AccessTokenTTL, b.cfg.RefreshTokenTTL).
		IssueSessionAccessToken(who.ID, other, &auth.Session{AuthTime: time.Now(), ID: "s1"})
	require.NoError(t, err)

	resp := do(t, &request{method: http.MethodPost, url: b.base + "/api/tenants/" + who.TenantID.String() + "/archive",
		body: `{"confirm_name":` + strconv.Quote(name) + `}`, token: token})
	assert.Equal(t, http.StatusOK, resp.code, resp.body)
}

// The intake listener is wired to the tenant archive check in newServices: an
// archived tenant's post is acknowledged, files nothing, and files again once
// unarchived.
func TestServe_AnArchivedTenantsIntakePostIsAcknowledgedAndDropped(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{intake: true})
	who, _ := b.signedInUser(t)
	ctx := db.WithTenant(context.Background(), who.TenantID)
	src := uuid.New()
	token, hash, err := intake.NewToken()
	require.NoError(t, err)
	_, err = intake.NewPostgresStore(b.appDB).CreateSource(ctx, src,
		&intake.Spec{Name: "boot", Adapter: "native", Trust: intake.TrustUntrusted, AuthKind: intake.AuthBearer,
			DefaultStatus: ticket.StatusPreparing, DefaultPriority: model.PriorityNormal, MaxPriority: model.PriorityNormal,
			RoleMatch: model.RoleMatchAny, ReopenPolicy: intake.ReopenNew},
		&intake.NewCredential{Kind: intake.AuthBearer, TokenHash: hash}, who.ID)
	require.NoError(t, err)
	post := func(key string) *response {
		return do(t, &request{method: http.MethodPost, url: "http://" + b.cfg.Intake.ListenAddr + "/hooks/intake/" + src.String(),
			body:   `{"key":"` + key + `","event":"open","title":"boot"}`,
			header: map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}})
	}
	tickets := ticket.NewService(ticket.NewPostgresStore(b.appDB), slog.DiscardHandler)

	dbtest.ArchiveTenant(t, b.appDB, who.TenantID)
	resp := post("k1")
	assert.Equal(t, http.StatusOK, resp.code, resp.body)
	assert.Contains(t, resp.body, "archived")
	all, err := tickets.List(ctx, &ticket.ListFilter{})
	require.NoError(t, err)
	assert.Empty(t, all)

	dbtest.UnarchiveTenant(t, b.appDB, who.TenantID)
	resp = post("k2")
	assert.Equal(t, http.StatusOK, resp.code, resp.body)
	assert.Contains(t, resp.body, "created as preparing")
	all, err = tickets.List(ctx, &ticket.ListFilter{})
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.NotNil(t, all[0].Intake)
	assert.Equal(t, "k2", all[0].Intake.Key)
}

// Enabling provision_from_board imports through the provisioning service,
// which newServices builds with the slot reports: unwired, the import is
// unavailable and the owner's write fails.
func TestServe_OwnerCanEnableProvisionFromBoard(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	who, token := b.signedInUser(t)
	ctx := db.WithTenant(context.Background(), who.TenantID)
	_, err := b.appDB.Global().Exec(ctx,
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'owner') ON CONFLICT DO NOTHING`, uuid.New(), who.TenantID, who.ID)
	require.NoError(t, err)
	// ImportAll only reads slot reports for a tenant that has a supervisor.
	capTwo := 2
	b.agentTokenFor(t, who, &oauth.Decision{Approve: true, AgentName: "box-1", Kind: "supervisor", SlotCap: &capTwo})

	resp := do(t, &request{method: http.MethodPatch, url: b.base + "/api/settings/provision_from_board",
		body: `{"type":"bool","bool_val":true}`, token: token})

	assert.Equal(t, http.StatusOK, resp.code, resp.body)
}
