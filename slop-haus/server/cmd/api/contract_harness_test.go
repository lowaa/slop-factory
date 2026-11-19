package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/auth"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The contract tests pin what the ticket lifecycle looks like to its callers:
// agents over MCP, a person on the board's REST API, and GitHub's webhook.
// They run against boot() with only the code host faked, and read nothing
// but what those callers are given -- never a table -- so the storage behind
// the lifecycle can be reshaped without touching them..
//
// One rig per test, each on its own database: tickets are numbered from 1 in
// a fresh database, which keeps the goldens stable, and a boot costs a few
// hundred milliseconds beyond the template clone the other serve tests pay.

var updateContractGolden = flag.Bool("update", false, "rewrite the contract tests' golden files from the current output")

// contractRig is a running api with one tenant, a signed-in person in it and
// the fake code host.
type contractRig struct {
	t    *testing.T
	boot *booted
	// user is the signed-in person; board calls are theirs.
	user  *authn.User
	token string

	host *contractHost

	mu      sync.Mutex
	workers int
	roles   map[string]string
	hook    *contractWebhook
}

func newContractRig(t *testing.T) *contractRig {
	t.Helper()
	return newContractRigWith(t, &bootOptions{})
}

// newContractRigWith is newContractRig with extra boot options (a clock, say);
// the rig fills in the webhooks and the fake code host.
func newContractRigWith(t *testing.T, opts *bootOptions) *contractRig {
	t.Helper()
	host := &contractHost{fake: &codehosttest.Fake{ChecksOf: map[codehost.ChangeRef]*codehost.Checks{}}}
	opts.webhooks, opts.changes = true, host
	b := boot(t, opts)
	who, _ := b.signedInUser(t)
	// An owner of their tenant, as whoever sets a tenant up is: owners alone
	// may declare the workspace manifest the change flow places a ticket by.
	_, err := b.appDB.Global().Exec(t.Context(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'owner')`,
		uuid.New(), who.TenantID, who.ID)
	require.NoError(t, err)
	// A session that has just signed in: the board asks for that before it
	// lets a person create a webhook or mark an agent a console.
	sessionID, err := auth.NewSessionID()
	require.NoError(t, err)
	token, err := auth.NewSigner([]byte(bootJWTSecret), b.cfg.AccessTokenTTL, b.cfg.RefreshTokenTTL).
		IssueSessionAccessToken(who.ID, who.TenantID, &auth.Session{ID: sessionID, AuthTime: time.Now()})
	require.NoError(t, err)
	return &contractRig{t: t, boot: b, user: who, token: token, host: host, roles: map[string]string{}}
}

// contractHost is the code host's operations as the api sees them: the fake
// behind a lock, so a test can reconfigure it between calls while the api's
// goroutines read it. Everything the api asks of the host is the fake's
// answer; it decides nothing.
type contractHost struct {
	mu   sync.Mutex
	fake *codehosttest.Fake
}

func (h *contractHost) OpenChange(ctx context.Context, tenant uuid.UUID, req *codehost.OpenRequest) (codehost.ChangeRef, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.OpenChange(ctx, tenant, req)
}

func (h *contractHost) MarkReady(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.MarkReady(ctx, tenant, ref)
}

func (h *contractHost) CloseChange(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.CloseChange(ctx, tenant, ref)
}

func (h *contractHost) RetargetChange(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef, base string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.RetargetChange(ctx, tenant, ref, base)
}

func (h *contractHost) RetitleChange(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef, title string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.RetitleChange(ctx, tenant, ref, title)
}

func (h *contractHost) DeleteBranch(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.DeleteBranch(ctx, tenant, repo, branch)
}

func (h *contractHost) Checks(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef) (*codehost.Checks, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.Checks(ctx, tenant, ref)
}

func (h *contractHost) CompareBase(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef, headSHA string) (*codehost.BaseComparison, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.CompareBase(ctx, tenant, ref, headSHA)
}

func (h *contractHost) ChangeState(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef) (*codehost.ChangeState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.ChangeState(ctx, tenant, ref)
}

func (h *contractHost) BranchContains(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch, sha string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.BranchContains(ctx, tenant, repo, branch, sha)
}

func (h *contractHost) Diff(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef, headSHA string) (*codehost.Diff, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.Diff(ctx, tenant, ref, headSHA)
}

func (h *contractHost) CompareCommits(ctx context.Context, tenant uuid.UUID, ref codehost.ChangeRef, fromSHA, toSHA string) (*codehost.Diff, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.CompareCommits(ctx, tenant, ref, fromSHA, toSHA)
}

func (h *contractHost) DefaultBranchHead(ctx context.Context, tenant uuid.UUID, repo codehost.Repo) (*codehost.BranchHead, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.DefaultBranchHead(ctx, tenant, repo)
}

func (h *contractHost) CreateBranch(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch, sha string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.CreateBranch(ctx, tenant, repo, branch, sha)
}

func (h *contractHost) TakesInBranchPast(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch, since, head string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.TakesInBranchPast(ctx, tenant, repo, branch, since, head)
}

func (h *contractHost) CommitsAhead(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, since, branch string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.CommitsAhead(ctx, tenant, repo, since, branch)
}

func (h *contractHost) CompareBranches(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, base, branch string) (*codehost.BranchComparison, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.CompareBranches(ctx, tenant, repo, base, branch)
}

func (h *contractHost) MergeBranch(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, into, from string) (*codehost.BranchMerge, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.MergeBranch(ctx, tenant, repo, into, from)
}

func (h *contractHost) BranchHead(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch string) (*codehost.BranchHead, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.BranchHead(ctx, tenant, repo, branch)
}

func (h *contractHost) BranchChecks(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch string) (*codehost.Checks, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.BranchChecks(ctx, tenant, repo, branch)
}

func (h *contractHost) TreeOf(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, sha string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.TreeOf(ctx, tenant, repo, sha)
}

func (h *contractHost) UpdateBranch(ctx context.Context, tenant uuid.UUID, req *codehost.UpdateBranchRequest) (*codehost.BranchUpdate, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.UpdateBranch(ctx, tenant, req)
}

func (h *contractHost) EnsureBranch(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, branch, sha string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.EnsureBranch(ctx, tenant, repo, branch, sha)
}

func (h *contractHost) ResolveRef(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, ref string) (*codehost.ResolvedRef, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.ResolveRef(ctx, tenant, repo, ref)
}

func (h *contractHost) CreateTag(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, name, sha string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.CreateTag(ctx, tenant, repo, name, sha)
}

func (h *contractHost) Merge(ctx context.Context, tenant uuid.UUID, req *codehost.MergeRequest) (*codehost.Merged, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.Merge(ctx, tenant, req)
}

// configure changes what the fake answers; it holds the lock the api's calls
// take.
func (h *contractHost) configure(edit func(f *codehosttest.Fake)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	edit(h.fake)
}

// opened sets the change the next OpenChange reports.
func (h *contractHost) opened(ref codehost.ChangeRef) {
	h.configure(func(f *codehosttest.Fake) { f.Opened = ref })
}

// checks sets what Checks answers for ref.
func (h *contractHost) checks(ref codehost.ChangeRef, c *codehost.Checks) {
	h.configure(func(f *codehosttest.Fake) { f.ChecksOf[ref] = c })
}

// mergedWith sets the merge commit a Merge answers.
func (h *contractHost) mergedWith(sha string) {
	h.configure(func(f *codehosttest.Fake) { f.MergedWith = codehost.Merged{SHA: sha} })
}

func (h *contractHost) opens() []codehosttest.OpenCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.Opens()
}

func (h *contractHost) merges() []codehosttest.OperationCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fake.Merges()
}

// -- the board, as the signed-in person --

// jsonObject is a decoded JSON object, for structural assertions on what the
// board answers.
type jsonObject = map[string]any

// boardResponse is the board's answer: the status code and the JSON body,
// which is nil for an answer with none.
type boardResponse struct {
	code int
	raw  string
	json any
}

// obj is the body as an object, failing the test if it is not one.
func (r *boardResponse) obj(t *testing.T) jsonObject {
	t.Helper()
	o, ok := r.json.(map[string]any)
	require.True(t, ok, "the body is not a JSON object (%d): %s", r.code, r.raw)
	return o
}

// boardDo calls the board's REST API as the signed-in person. body is
// encoded as JSON when not nil.
func (r *contractRig) boardDo(method, path string, body any) *boardResponse {
	r.t.Helper()
	var payload string
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(r.t, err)
		payload = string(b)
	}
	resp := do(r.t, &request{method: method, url: r.boot.base + path, token: r.token, body: payload})
	out := &boardResponse{code: resp.code, raw: resp.body}
	if strings.TrimSpace(resp.body) != "" {
		_ = json.Unmarshal([]byte(resp.body), &out.json)
	}
	return out
}

// mustBoard is boardDo that fails the test unless the answer is code.
func (r *contractRig) mustBoard(code int, method, path string, body any) *boardResponse {
	r.t.Helper()
	resp := r.boardDo(method, path, body)
	require.Equal(r.t, code, resp.code, "%s %s: %s", method, path, resp.raw)
	return resp
}

// openTicket files a ticket as open through the board; fields are
// CreateTicketBody's, and the title is required.
func (r *contractRig) openTicket(fields jsonObject) jsonObject {
	r.t.Helper()
	body := jsonObject{"status": "open"}
	for k, v := range fields {
		body[k] = v
	}
	return r.mustBoard(http.StatusCreated, http.MethodPost, "/api/tickets", body).obj(r.t)
}

// ticket reads a ticket as the board shows it.
func (r *contractRig) ticket(id int64) jsonObject {
	r.t.Helper()
	return r.mustBoard(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/tickets/%d", id), nil).obj(r.t)
}

// ticketID is the id of a ticket body.
func ticketID(t *testing.T, tk jsonObject) int64 {
	t.Helper()
	id, ok := tk["id"].(float64)
	require.True(t, ok, "no id in %v", tk)
	return int64(id)
}

// declareRepo declares the tenant's workspace as one repository checked out
// in the server/ subfolder, with the given remote (github.com/acme/repo), so a ticket's
// scopes place in it and its change opens there.
func (r *contractRig) declareRepo(remote string) {
	r.t.Helper()
	r.mustBoard(http.StatusOK, http.MethodPut, "/api/workspace/manifest",
		jsonObject{"repos": []jsonObject{{"path": "server", "remote": remote}}})
}

// role is the id of the agent role called name, created through the board
// the first time it is asked for.
func (r *contractRig) role(name string) string {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.roles[name]; ok {
		return id
	}
	created := r.mustBoard(http.StatusCreated, http.MethodPost, "/api/agent-roles", jsonObject{"name": name}).obj(r.t)
	id := created["id"].(string)
	r.roles[name] = id
	return id
}

// agentBody is the board's view of the named agent: its lease, session and
// roles.
func (r *contractRig) agentBody(name string) jsonObject {
	r.t.Helper()
	listed := r.mustBoard(http.StatusOK, http.MethodGet, "/api/agents", nil).obj(r.t)
	for _, a := range listed["agents"].([]any) {
		if a := a.(map[string]any); a["name"] == name {
			return a
		}
	}
	r.t.Fatalf("the board lists no agent named %q", name)
	return nil
}

// -- agents, over MCP --

// contractAgent is an agent with a real OAuth token and an MCP session
// over HTTP.
type contractAgent struct {
	rig     *contractRig
	name    string
	id      string
	session *mcp.ClientSession
	token   string
}

// worker signs in a new agent, with the given roles assigned by the person.
func (r *contractRig) worker(roles ...string) *contractAgent {
	r.t.Helper()
	r.mu.Lock()
	r.workers++
	name := fmt.Sprintf("worker-%d", r.workers)
	r.mu.Unlock()
	return r.agentNamed(name, roles...)
}

func (r *contractRig) agentNamed(name string, roles ...string) *contractAgent {
	r.t.Helper()
	return r.agentOfKind(name, "", roles...)
}

// agentOfKind registers the agent through the consent flow, as a real client
// does; kind is "" for a worker.
func (r *contractRig) agentOfKind(name, kind string, roles ...string) *contractAgent {
	r.t.Helper()
	token := r.boot.agentTokenFor(r.t, r.user, &oauth.Decision{Approve: true, AgentName: name, Kind: kind})
	a := &contractAgent{rig: r, name: name, token: token}
	a.id = r.agentBody(name)["id"].(string)
	if len(roles) > 0 {
		ids := make([]string, 0, len(roles))
		for _, role := range roles {
			ids = append(ids, r.role(role))
		}
		r.mustBoard(http.StatusOK, http.MethodPut, "/api/agents/"+a.id+"/roles", jsonObject{"role_ids": ids})
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "contract-" + name, Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: r.boot.base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: token}},
	}, nil)
	require.NoError(r.t, err)
	r.t.Cleanup(func() { _ = session.Close() })
	a.session = session
	return a
}

// bearerTransport signs every request with an agent's access token.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(req)
}

// toolResult is a tool's answer as the agent reads it.
type toolResult struct {
	isError    bool
	text       string
	structured jsonObject
	// rendered is the answer as a golden file holds it, before ids are
	// replaced.
	rendered string
}

// call runs a tool as the agent. A tool that fails is a result with isError
// set, which the agent reads like any other; only a broken transport or an
// invalid call fails the test.
func (a *contractAgent) call(tool string, args jsonObject) *toolResult {
	a.rig.t.Helper()
	res, err := a.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	require.NoError(a.rig.t, err, "%s as %s", tool, a.name)
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	structured, err := json.MarshalIndent(res.StructuredContent, "", "  ")
	require.NoError(a.rig.t, err)
	out := &toolResult{isError: res.IsError, text: text.String(),
		rendered: fmt.Sprintf("-- error: %t --\n-- text --\n%s\n-- structured --\n%s\n", res.IsError, text.String(), structured)}
	if obj, ok := res.StructuredContent.(map[string]any); ok {
		out.structured = obj
	}
	return out
}

// complete closes the agent's ticket through the ticket service, bypassing
// ticket_finish's rules (promotion, the landing queue, the secret scan): no
// agent-facing route does that any more. For tests that need a finished
// ticket and are not about finishing.
func (a *contractAgent) complete(args jsonObject) *toolResult {
	a.rig.t.Helper()
	agentID, err := uuid.Parse(a.id)
	require.NoError(a.rig.t, err)
	summary, _ := args["handoff"].(string)
	prURL, _ := args["pr_url"].(string)
	svc := ticket.NewService(ticket.NewPostgresStore(a.rig.boot.appDB), slog.DiscardHandler)
	ctx := db.WithTenant(a.rig.t.Context(), a.rig.user.TenantID)
	t, err := svc.Done(ctx, agentID, &ticket.DoneParams{Summary: summary, PRURL: prURL})
	if err != nil {
		return &toolResult{isError: true, text: err.Error()}
	}
	return &toolResult{text: "done: ticket " + strconv.FormatInt(t.ID, 10)}
}

// mustComplete is complete that fails the test if the close is refused.
func (a *contractAgent) mustComplete(args jsonObject) *toolResult {
	a.rig.t.Helper()
	res := a.complete(args)
	require.False(a.rig.t, res.isError, "complete as %s: %s", a.name, res.text)
	return res
}

// mustCall is call that fails the test if the tool reports an error.
func (a *contractAgent) mustCall(tool string, args jsonObject) *toolResult {
	a.rig.t.Helper()
	res := a.call(tool, args)
	require.False(a.rig.t, res.isError, "%s as %s: %s", tool, a.name, res.text)
	return res
}

// -- GitHub's webhook --

const (
	appInstallationID = 4242
	appWebhookSecret  = "the-apps-webhook-secret"
)

// contractWebhook is where the tenant's GitHub App delivers, and the secret
// it signs with.
type contractWebhook struct {
	url    string
	secret string
}

// webhook is the tenant's GitHub App webhook, connected with a stored
// secret the first time it is asked for, as the credential service would.
func (r *contractRig) webhook() *contractWebhook {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hook == nil {
		raw, err := base64.StdEncoding.DecodeString(bootCredentialKey)
		require.NoError(r.t, err)
		keyring, err := secretbox.NewKeyring(raw, nil)
		require.NoError(r.t, err)
		id := uuid.New()
		sealed, err := keyring.Seal([]byte(appWebhookSecret), secretbox.Binding{Tenant: r.user.TenantID, Row: id, Purpose: "webhook secret"})
		require.NoError(r.t, err)
		_, err = r.boot.appDB.Exec(db.WithTenant(r.t.Context(), r.user.TenantID), `
			INSERT INTO codehost_apps (id, provider, app_id, private_key_enc, key_id, key_fingerprint,
			                           webhook_secret_enc, webhook_secret_key_id)
			VALUES ($1, 'github_app', 1, '\x00', $2, $3, $4, $2)`,
			id, keyring.CurrentID(), strings.Repeat("f", 64), sealed)
		require.NoError(r.t, err)
		_, err = r.boot.appDB.Exec(db.WithTenant(r.t.Context(), r.user.TenantID), `
			INSERT INTO codehost_credentials (id, app_ref, installation_id, binding) VALUES (gen_random_uuid(), $1, $2, 'github.com')`,
			id, appInstallationID)
		require.NoError(r.t, err)
		r.hook = &contractWebhook{url: "http://" + r.boot.cfg.Webhook.ListenAddr + githubhook.AppReceivePath, secret: appWebhookSecret}
	}
	return r.hook
}

// deliver posts a signed delivery of a GitHub event, as GitHub does, and
// answers the status the receiver gave.
func (r *contractRig) deliver(event string, payload jsonObject) int {
	r.t.Helper()
	return r.send(&post{event: event, body: payload}).code
}

// deliveries are the webhook deliveries the board lists, newest first.
func (r *contractRig) deliveries() []any {
	r.t.Helper()
	listed := r.mustBoard(http.StatusOK, http.MethodGet, "/api/github/webhooks", nil).obj(r.t)
	return listed["deliveries"].([]any)
}

// -- goldens --

var (
	uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	timePattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
)

// stable replaces what differs from run to run: the api's address, ids,
// timestamps. Ticket numbers do not differ -- each rig starts a database --
// so they stay in the text, where they are part of what the agent reads.
func (r *contractRig) stable(s string) string {
	s = strings.ReplaceAll(s, r.boot.base, "{board}")
	s = strings.ReplaceAll(s, r.user.TenantID.String(), "{tenant}")
	s = uuidPattern.ReplaceAllString(s, "{uuid}")
	return timePattern.ReplaceAllString(s, "{time}")
}

// assertGolden compares what an agent read with
// testdata/golden/<name>.golden, or rewrites the file under -update.
func (r *contractRig) assertGolden(name string, res *toolResult) {
	r.t.Helper()
	assertContractGolden(r.t, name, r.stable(res.rendered))
}

func assertContractGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".golden")
	if *updateContractGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "no golden file; run with -update to create it")
	assert.Equal(t, string(want), got, "%s differs from its golden file", name)
}

// -- the harness itself --

// The rig's pieces stand on their own before a lifecycle is built on them.
func TestContractHarness_Pieces(t *testing.T) {
	t.Parallel()
	rig := newContractRig(t)

	t.Run("an agent over MCP and the board see the same ticket", func(t *testing.T) {
		tk := rig.openTicket(jsonObject{"title": "Harness ticket"})
		w := rig.worker()
		checkin := w.mustCall("checkin", nil)
		assert.Equal(t, w.name, checkin.structured["agent"])
		assert.Contains(t, checkin.text, "holding nothing")
		assert.Equal(t, "open", rig.ticket(ticketID(t, tk))["status"])
	})

	t.Run("a webhook delivery is signed and received", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, rig.deliver("ping", jsonObject{"zen": "test"}))
		deliveries := rig.deliveries()
		require.Len(t, deliveries, 1)
		assert.Equal(t, "ping", deliveries[0].(map[string]any)["event"])
	})

	t.Run("a delivery signed with another secret is refused", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, rig.send(&post{event: "ping", body: jsonObject{"zen": "test"}, secret: "not-the-secret"}).code)
	})

	t.Run("the fake answers what the test configured", func(t *testing.T) {
		ref := codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "repo", Number: 12}
		rig.host.opened(ref)
		rig.host.checks(ref, codehost.NewChecks("abc123", "main", []codehost.Check{{Name: "ci", State: codehost.CheckSuccess}}))
		rig.host.mergedWith("def456")

		opened, err := rig.host.OpenChange(t.Context(), rig.user.TenantID, &codehost.OpenRequest{})
		require.NoError(t, err)
		assert.Equal(t, ref, opened)
		checks, err := rig.host.Checks(t.Context(), rig.user.TenantID, ref)
		require.NoError(t, err)
		assert.True(t, checks.Passed())
		merged, err := rig.host.Merge(t.Context(), rig.user.TenantID, &codehost.MergeRequest{Change: ref, HeadSHA: "abc123"})
		require.NoError(t, err)
		assert.Equal(t, "def456", merged.SHA)
		assert.Len(t, rig.host.merges(), 1)
	})

	t.Run("the change flow reaches the fake through the api", func(t *testing.T) {
		rig := newContractRig(t)
		rig.declareRepo("github.com/acme/repo")
		id := ticketID(t, rig.openTicket(jsonObject{"title": "Opens a change", "paths": []jsonObject{{"path": "server/"}}}))
		w := rig.worker()
		w.mustCall("ticket_next", nil)
		ref := codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "repo", Number: 12}
		rig.host.opened(ref)

		opened := w.mustCall("change_open", jsonObject{"branch": "ticket-1-a1", "title": "Opens a change", "draft": true})
		const link = "https://github.com/acme/repo/pull/12"
		assert.Equal(t, link, opened.structured["url"], "the link is the real host's reading of the fake's change")
		assert.Equal(t, true, opened.structured["draft"])
		calls := rig.host.opens()
		require.Len(t, calls, 1)
		assert.Equal(t, rig.user.TenantID, calls[0].Tenant)
		assert.Equal(t, "acme", calls[0].Request.Repo.Owner)
		assert.Equal(t, "repo", calls[0].Request.Repo.Name)
		assert.Equal(t, "ticket-1-a1", calls[0].Request.Head)
		assert.True(t, calls[0].Request.Draft)
		assert.Equal(t, link, rig.ticket(id)["pr_url"])
	})

	t.Run("goldens are rewritten under -update and stable", func(t *testing.T) {
		got := rig.stable(fmt.Sprintf("%s/tickets/1?tenant=%s at 2026-10-08T14:17:32Z by %s", rig.boot.base, rig.user.TenantID, uuid.NewString()))
		assert.Equal(t, "{board}/tickets/1?tenant={tenant} at {time} by {uuid}", got)
	})
}
