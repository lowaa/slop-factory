package attachment_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

type testAgent struct {
	id, tenant uuid.UUID
	console    bool
	conductor  bool
}

func (a testAgent) AgentID() uuid.UUID  { return a.id }
func (a testAgent) AgentName() string   { return "agent-" + a.id.String() }
func (a testAgent) TenantID() uuid.UUID { return a.tenant }
func (a testAgent) ReadOnly() bool      { return false }
func (a testAgent) IsConductor() bool   { return a.conductor }
func (a testAgent) IsConsole() bool     { return a.console }

type heldTicket map[uuid.UUID]int64

func (h heldTicket) HeldTicket(_ context.Context, id uuid.UUID) (int64, bool, error) {
	t, ok := h[id]
	return t, ok, nil
}

type agentRig struct {
	*fixture
	mux    *http.ServeMux
	agents map[string]testAgent
	held   heldTicket
}

func newAgentRig(t *testing.T) *agentRig {
	t.Helper()
	f := newFixture(t)
	r := &agentRig{fixture: f, mux: http.NewServeMux(), agents: map[string]testAgent{}, held: heldTicket{}}
	f.svc = attachment.NewService(&attachment.Options{Repo: f.repo, Store: f.store, Quota: quotaFunc(func() int64 { return f.quota }), Holder: r.held, TempDir: t.TempDir()})
	api := humago.New(r.mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.HumaMiddleware(api, func(_ context.Context, token string) (authn.Identity, error) {
		if a, ok := r.agents[token]; ok {
			return a, nil
		}
		return nil, errors.New("unknown token")
	}, audit.New(slog.DiscardHandler, nil), logger))
	attachment.RegisterAgentRoutes(api, attachment.NewHandler(f.svc, slog.DiscardHandler))
	return r
}

// signIn makes an agent of the tenant, with the row the uploader's foreign key needs.
func (r *agentRig) signIn(tn *tenantRig, console bool) (string, testAgent) {
	id := uuid.New()
	_, err := r.db.Exec(tn.ctx, `INSERT INTO agents (id, name) VALUES ($1, $2)`, id, "a-"+id.String())
	require.NoError(r.t, err)
	token := uuid.NewString()
	a := testAgent{id: id, tenant: tn.id, console: console}
	r.agents[token] = a
	return token, a
}

func (r *agentRig) post(t *testing.T, path, token, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := multipartBody(t, "file", filename, content)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func (r *agentRig) get(path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func agentPath(ticket int64) string { return "/api/agent/tickets/" + itoa(ticket) + "/attachments" }

func TestAgentHTTPWorkerUploadsToItsTicketAndDownloads(t *testing.T) {
	r := newAgentRig(t)
	tn := r.newTenant()
	token, who := r.signIn(tn, false)
	r.held[who.id] = tn.ticket

	rec := r.post(t, agentPath(tn.ticket), token, "frame.png", pngBytes)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := decode[attachment.AttachmentBody](t, rec)
	assert.Equal(t, "agent", created.UploaderKind)
	require.NotNil(t, created.UploaderID)
	assert.Equal(t, who.id, *created.UploaderID)

	dl := r.get("/api/agent/attachments/"+created.ID.String(), token)
	require.Equal(t, http.StatusOK, dl.Code)
	assert.Equal(t, pngBytes, dl.Body.Bytes())
	assert.Equal(t, "nosniff", dl.Header().Get("X-Content-Type-Options"))
}

func TestAgentHTTPRefusals(t *testing.T) {
	r := newAgentRig(t)
	tn := r.newTenant()
	other := r.newTenant()
	worker, wa := r.signIn(tn, false)
	console, _ := r.signIn(tn, true)
	foreign, _ := r.signIn(other, true)
	r.held[wa.id] = tn.ticket
	// Ticket numbers are per tenant, so a second ticket here has no namesake in the other tenant.
	var second int64
	require.NoError(t, r.db.QueryRow(tn.ctx, `INSERT INTO tickets (title) VALUES ('second') RETURNING id`).Scan(&second))
	require.NotEqual(t, other.ticket, second)

	assert.Equal(t, http.StatusUnauthorized, r.post(t, agentPath(tn.ticket), "", "a.png", pngBytes).Code, "no token")
	assert.Equal(t, http.StatusForbidden, r.post(t, agentPath(second), worker, "a.png", pngBytes).Code, "a worker holding another ticket")
	assert.Equal(t, http.StatusUnprocessableEntity, r.post(t, agentPath(tn.ticket), worker, "ghp_"+"a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1.png", pngBytes).Code, "credential in the filename")
	assert.Equal(t, http.StatusUnprocessableEntity, r.post(t, agentPath(tn.ticket), worker, "a.html", htmlBytes).Code, "not an image")

	ok := r.post(t, agentPath(tn.ticket), console, "c.png", pngBytes)
	require.Equal(t, http.StatusCreated, ok.Code, "a console may attach to any ticket of its tenant")
	id := decode[attachment.AttachmentBody](t, ok).ID

	conductor := uuid.NewString()
	cid := uuid.New()
	_, err := r.db.Exec(tn.ctx, `INSERT INTO agents (id, name) VALUES ($1, $2)`, cid, "c-"+cid.String())
	require.NoError(t, err)
	r.agents[conductor] = testAgent{id: cid, tenant: tn.id, conductor: true}
	assert.Equal(t, http.StatusCreated, r.post(t, agentPath(second), conductor, "d.png", pngBytes).Code, "a conductor may attach to any ticket of its tenant")

	assert.Equal(t, http.StatusNotFound, r.post(t, agentPath(second), foreign, "x.png", pngBytes).Code, "another tenant's ticket")
	assert.Equal(t, http.StatusNotFound, r.get("/api/agent/attachments/"+id.String(), foreign).Code, "another tenant's attachment")
	assert.Equal(t, 2, r.count(tn, `SELECT count(*) FROM attachments`))
}
