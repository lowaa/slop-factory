package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
)

// A lease records what the server served the session it was granted to, from
// the resolution the workspace's harness_config answers from, and an agent in
// a variant claims only what is routed to its version: wired together, so
// booted.
func TestServe_LeaseRecordsServedConfigAndVariantAgentsClaimOnlyWhatIsRoutedToThem(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	owner, ownerToken := b.signedInUser(t)
	_, err := b.appDB.Global().Exec(t.Context(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'owner')`,
		uuid.New(), owner.TenantID, owner.ID)
	require.NoError(t, err)
	plainToken := b.agentTokenFor(t, owner, &oauth.Decision{Approve: true, AgentName: "plain-agent"})
	variantToken := b.agentTokenFor(t, owner, &oauth.Decision{Approve: true, AgentName: "variant-agent"})
	ctx := db.WithTenant(t.Context(), owner.TenantID)

	call := func(method, path, token, body string) *response {
		t.Helper()
		return do(t, &request{method: method, url: b.base + path, token: token, body: body})
	}
	idOf := func(resp *response) string {
		t.Helper()
		require.Equal(t, http.StatusCreated, resp.code, resp.body)
		var v struct {
			// A version's id is a string and a ticket's a number.
			ID any `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(resp.body), &v))
		return fmt.Sprint(v.ID)
	}

	var listed struct {
		Agents []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"agents"`
	}
	resp := call(http.MethodGet, "/api/agents", ownerToken, "")
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	require.NoError(t, json.Unmarshal([]byte(resp.body), &listed))
	ids := map[string]uuid.UUID{}
	for _, a := range listed.Agents {
		ids[a.Name] = uuid.MustParse(a.ID)
	}
	plain, variantAgent := ids["plain-agent"], ids["variant-agent"]
	require.NotEqual(t, uuid.Nil, plain)
	require.NotEqual(t, uuid.Nil, variantAgent)

	require.Equal(t, http.StatusOK, call(http.MethodPut, "/api/harness_config", ownerToken, `{"settings":{"claude":{"model":"opus"}}}`).code)
	fleet := idOf(call(http.MethodPost, "/api/fleet_instructions/versions", ownerToken, `{"name":"house","body":"Default text.","make_default":true}`))
	version := idOf(call(http.MethodPost, "/api/variants/versions", ownerToken,
		`{"name":"pi-fast","harness":"pi","settings":{"model":"qwen/qwen3:free"},"fleet_instructions_version_id":null}`))
	require.Equal(t, http.StatusNoContent, call(http.MethodPut, "/api/agents/"+variantAgent.String()+"/variant", ownerToken, `{"version_id":"`+version+`"}`).code)
	ticketIDs := map[string]string{}
	for _, title := range []string{"first", "second"} {
		ticketIDs[title] = idOf(call(http.MethodPost, "/api/tickets", ownerToken, `{"title":"`+title+`","status":"open","paths":[{"path":"`+title+`/"}]}`))
	}

	type recorded struct {
		Settings string
		Variant  *uuid.UUID
		Fleet    *uuid.UUID
	}
	record := func(agent uuid.UUID) (recorded, bool) {
		var r recorded
		err := b.appDB.QueryRow(ctx, `
			SELECT c.settings::text, c.variant_id, c.fleet_instructions_version_id
			  FROM lease_configs c JOIN leases l ON l.id = c.lease_id AND l.tenant_id = c.tenant_id
			 WHERE l.agent_id = $1`, agent).Scan(&r.Settings, &r.Variant, &r.Fleet)
		return r, err == nil
	}

	served := call(http.MethodGet, "/api/workspace/harness_config", plainToken, "")
	require.Equal(t, http.StatusOK, served.code, served.body)
	var servedBody struct {
		Settings json.RawMessage `json:"settings"`
	}
	require.NoError(t, json.Unmarshal([]byte(served.body), &servedBody))

	claim := call(http.MethodPost, "/api/claims", plainToken, `{}`)
	require.Equal(t, http.StatusOK, claim.code, claim.body)
	got, ok := record(plain)
	require.True(t, ok, "a default-fleet agent's lease records its configuration")
	assert.JSONEq(t, string(servedBody.Settings), got.Settings, "recorded is what harness_config served")
	assert.Nil(t, got.Variant)
	require.NotNil(t, got.Fleet)
	assert.Equal(t, fleet, got.Fleet.String())

	require.Equal(t, http.StatusOK, call(http.MethodPut, "/api/harness_config", ownerToken, `{"settings":{"claude":{"model":"sonnet"}}}`).code)
	after, _ := record(plain)
	assert.Equal(t, got, after, "a later config change does not touch the record")

	sessions := session.NewPostgresStore(b.appDB)
	_, err = sessions.Start(ctx, variantAgent, "variant-session", "/work")
	require.NoError(t, err)
	describe := func(harness string) {
		t.Helper()
		require.NoError(t, sessions.Describe(ctx, variantAgent, "variant-session", &session.Description{Harness: harness}))
	}

	describe("claude")
	refused := call(http.MethodPost, "/api/claims", variantToken, `{}`)
	assert.Equal(t, http.StatusConflict, refused.code, refused.body)
	assert.Contains(t, refused.body, "runs the pi harness")
	assert.Contains(t, refused.body, "this session runs claude")

	describe("pi")
	idle := call(http.MethodPost, "/api/claims", variantToken, `{}`)
	require.Equal(t, http.StatusOK, idle.code, idle.body)
	_, held := record(variantAgent)
	assert.False(t, held, "nothing names the variant yet, and the unrouted ticket is not its to take")

	require.Equal(t, http.StatusOK, call(http.MethodPut, "/api/tickets/"+ticketIDs["second"]+"/variant", ownerToken, `{"version_id":"`+version+`"}`).code)
	routed := call(http.MethodPost, "/api/claims", variantToken, `{}`)
	require.Equal(t, http.StatusOK, routed.code, routed.body)
	assert.Contains(t, routed.body, `"title":"second"`)
	got, held = record(variantAgent)
	require.True(t, held)
	require.NotNil(t, got.Variant)
	assert.Equal(t, version, got.Variant.String(), "the lease records the variant it ran on")
}
