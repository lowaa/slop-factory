package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Owner-bound blocker editing only works if serve wires the resolver into the
// ticket service, which no service-level test can see: a conductor of a tenant
// member edits a blocker on a ticket it does not hold, a worker does not.
func TestServe_ConductorEditsBlockersOnATicketItDoesNotHold(t *testing.T) {
	t.Parallel()
	logs := &attrLog{}
	b := boot(t, &bootOptions{logs: logs})
	who, _ := b.signedInUser(t)
	ctx := db.WithTenant(context.Background(), who.TenantID)
	tickets := ticket.NewService(ticket.NewPostgresStore(b.appDB), slog.DiscardHandler)
	open := func(title string) *ticket.Ticket {
		tk, err := tickets.Create(ctx, &ticket.CreateParams{Title: title, Status: ticket.StatusOpen})
		require.NoError(t, err)
		return tk
	}
	target, blocker := open("Target"), open("Blocker")

	_, err := b.appDB.Global().Exec(t.Context(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'member')`,
		uuid.New(), who.TenantID, who.ID)
	require.NoError(t, err)
	conductorID := uuid.New()
	_, err = b.appDB.Exec(ctx, `INSERT INTO agents (id, name, kind, owner_user_id) VALUES ($1, 'conductor', 'conductor', $2)`, conductorID, who.ID)
	require.NoError(t, err)

	workerToken := b.agentTokenFor(t, who, &oauth.Decision{Approve: true, AgentName: "worker"})
	conductorToken := b.agentTokenFor(t, who, &oauth.Decision{Approve: true, AgentID: &conductorID})
	call := func(token, tool string) string {
		resp := do(t, &request{method: http.MethodPost, url: b.base + "/mcp", token: token, body: mcpInitialize,
			header: map[string]string{"Accept": "application/json, text/event-stream"}})
		require.Equal(t, http.StatusOK, resp.code, resp.body)
		sid := resp.header.Get("Mcp-Session-Id")
		h := map[string]string{"Accept": "application/json, text/event-stream", "Mcp-Session-Id": sid}
		_ = do(t, &request{method: http.MethodPost, url: b.base + "/mcp", token: token, header: h,
			body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`})
		return do(t, &request{method: http.MethodPost, url: b.base + "/mcp", token: token, header: h,
			body: `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` + tool +
				`","arguments":{"ticket_id":` + fmt.Sprint(target.ID) + `,"blocker_id":` + fmt.Sprint(blocker.ID) + `}}}`}).body
	}

	assert.Contains(t, call(workerToken, "ticket_blocker_add"), "isError\":true", "a worker keeps the worker rule")
	assert.Contains(t, call(conductorToken, "ticket_blocker_add"), "now waits on")
	assert.Contains(t, call(conductorToken, "ticket_blocker_remove"), "no longer waits on")
	audited := func(op string) bool {
		for _, rec := range logs.all() {
			if strings.Contains(rec, "action=owner_bound_blocker_changed") && strings.Contains(rec, "op="+op) &&
				strings.Contains(rec, "on_behalf_of_user_id="+who.ID.String()) {
				return true
			}
		}
		return false
	}
	assert.True(t, audited("add"), "the add is audited with the user it acts for")
	assert.True(t, audited("remove"), "the remove is audited with the user it acts for")
}

// attrLog keeps each record as its message and attributes flattened, so a
// test can look for an audit event's fields.
type attrLog struct {
	mu   sync.Mutex
	recs []string
}

func (h *attrLog) Enabled(context.Context, slog.Level) bool { return true }
func (h *attrLog) Handle(_ context.Context, r slog.Record) error {
	line := r.Message
	r.Attrs(func(a slog.Attr) bool { line += " " + a.Key + "=" + a.Value.String(); return true })
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, line)
	return nil
}
func (h *attrLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *attrLog) WithGroup(string) slog.Handler      { return h }

func (h *attrLog) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.recs...)
}
