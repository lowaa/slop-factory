package agent_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// fakeTokenResolver is a minimal agent.Grants: a token maps
// directly to the tenant and agent it was issued for. It stands in for
// internal/oauth here -- these tests are about what Service.Authenticate
// and the routes behind it do once a token has resolved, not about how
// OAuth performs that resolution, which is internal/oauth's own tests.
type fakeTokenResolver struct {
	mu    sync.Mutex
	byTok map[string]struct{ tenant, agent uuid.UUID }
}

func newFakeTokenResolver() *fakeTokenResolver {
	return &fakeTokenResolver{byTok: map[string]struct{ tenant, agent uuid.UUID }{}}
}

// issue mints a token for an agent, the way a completed OAuth grant would.
func (f *fakeTokenResolver) issue(tenant, agentID uuid.UUID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	token := "access-" + uuid.NewString()
	f.byTok[token] = struct{ tenant, agent uuid.UUID }{tenant, agentID}
	return token
}

func (f *fakeTokenResolver) Authenticate(_ context.Context, token string) (*oauth.Access, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cred, ok := f.byTok[token]
	if !ok {
		return nil, errors.New("unknown token")
	}
	return &oauth.Access{TenantID: cred.tenant, AgentID: cred.agent}, nil
}

func (f *fakeTokenResolver) RevokeAgentGrants(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

var _ agent.Grants = (*fakeTokenResolver)(nil)

// newAuthedServer is the agent routes behind the same agent-token
// middleware cmd/api installs, served over real HTTP -- the update is
// fetched by curl from inside a workspace, so that is how these fetch it.
func newAuthedServer(t *testing.T) (*httptest.Server, *fakeAgentStore, *fakeTokenResolver) {
	t.Helper()
	return newAuthedServerWithManifest(t, nil)
}

func newAuthedServerWithManifest(t *testing.T, manifest agent.ManifestRepos) (*httptest.Server, *fakeAgentStore, *fakeTokenResolver) {
	t.Helper()
	return newAuthedServerWith(t, manifest, nil)
}

func newAuthedServerWith(t *testing.T, manifest agent.ManifestRepos, fleet agent.FleetInstructions, tweaks ...func(*agent.HandlerDeps)) (*httptest.Server, *fakeAgentStore, *fakeTokenResolver) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := newFakeAgentStore()
	tokens := newFakeTokenResolver()
	agentSvc := agent.NewService(store, &fakeReleaser{}, tokens, logger.Handler())
	ticketSvc := &fakeAgentTicketStore{leases: map[uuid.UUID]*ticket.Lease{}}

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.HumaMiddleware(api, func(ctx context.Context, token string) (authn.Identity, error) {
		return agentSvc.Authenticate(ctx, token)
	}, audit.New(slog.DiscardHandler, nil), logger))
	deps := &agent.HandlerDeps{
		Agents: agentSvc, Roles: agent.NewRoleService(noRoles{}, slog.DiscardHandler), Tickets: ticketSvc, Sessions: &fakeSessions{},
		Manifest: manifest, Fleet: fleet,
	}
	for _, tweak := range tweaks {
		tweak(deps)
	}
	agent.RegisterRoutes(api, agent.NewHandler(deps, slog.DiscardHandler))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, store, tokens
}

// setUpWorkspace registers an agent through the setup endpoint and runs the
// script it returns in a fresh directory, as a human would.
func setUpWorkspace(t *testing.T, srv *httptest.Server, name string) string {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/agents/setup_script", "application/json",
		strings.NewReader(`{"name": "`+name+`"}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	script, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "body: %s", script)

	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "setup.sh")
	require.NoError(t, os.WriteFile(path, script, 0o755))
	cmd := exec.Command("bash", path)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "setup failed:\n%s", out)
	return dir
}

// agentIDByName looks an agent up the way the fake store's own tests do,
// standing in for the real onboarding flow's device login: production
// hands a client a credential through OAuth once the human approves it,
// which is out of scope here, so a test that needs a working token issues
// one against the fake resolver directly, keyed by the agent the setup
// endpoint actually created.
func agentIDByName(t *testing.T, store *fakeAgentStore, name string) uuid.UUID {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	id, ok := store.byName[name]
	require.True(t, ok, "no such agent: %s", name)
	return id
}

func getUpdate(t *testing.T, srv *httptest.Server, token string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/workspace/update_script", nil)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

// The whole path a human takes: set a workspace up, let it drift, run the
// updater that setup installed. Everything between is real -- the updater's
// curl, the agent-token middleware, the endpoint, the script it renders --
// except the credential itself: setup no longer embeds a working one (an
// agent is authorized through OAuth's device flow, which is out of scope
// here), so the updater is handed one the way SLOP_HAUS_TOKEN always could
// be, as an env var override that takes priority over the empty line in
// .slop-haus.env. See update.sh.
func TestUpdateScript_TheInstalledUpdaterBringsAWorkspaceBackUpToDate(t *testing.T) {
	srv, store, tokens := newAuthedServer(t)
	dir := setUpWorkspace(t, srv, "builder-1")

	worker := filepath.Join(dir, filepath.FromSlash(workspace.HeadlessBriefing))
	want, err := os.ReadFile(worker)
	require.NoError(t, err)
	require.NoError(t, os.Remove(worker))
	briefing := filepath.Join(dir, filepath.FromSlash(workspace.HeadlessBriefing))
	require.NoError(t, os.WriteFile(briefing, []byte("stale\n"), 0o644))

	token := tokens.issue(store.tenantID, agentIDByName(t, store, "builder-1"))
	envPath := filepath.Join(dir, filepath.FromSlash(workspace.EnvFile))
	envBefore := readFile(t, envPath)

	cmd := exec.Command(filepath.Join(dir, filepath.FromSlash(workspace.Updater)))
	// Somewhere else entirely: the updater works on the workspace it lives
	// in, not on wherever it was run from.
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "SLOP_HAUS_TOKEN="+token, "SLOP_HAUS_URL=")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "update failed:\n%s", out)

	got, err := os.ReadFile(worker)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
	assert.Contains(t, readFile(t, briefing), "slop-haus agent: builder-1")
	assert.Equal(t, envBefore, readFile(t, envPath),
		"the credential file is not something an update rewrites, whatever it holds")
}

// The script is the caller's own: the name in it comes from the token, and
// nothing in it is a secret.
func TestUpdateScript_IsTheCallingAgentsAndCarriesNoSecret(t *testing.T) {
	srv, store, tokens := newAuthedServer(t)
	setUpWorkspace(t, srv, "builder-7")
	setUpWorkspace(t, srv, "someone-else")

	theirToken := tokens.issue(store.tenantID, agentIDByName(t, store, "someone-else"))
	myToken := tokens.issue(store.tenantID, agentIDByName(t, store, "builder-7"))

	// Each token gets its own agent's script -- asked both ways, so a
	// handler that ignored the caller and rendered any fixed name fails.
	_, theirs := getUpdate(t, srv, theirToken)
	assert.Contains(t, theirs, "someone-else")
	assert.NotContains(t, theirs, "builder-7")

	resp, script := getUpdate(t, srv, myToken)

	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", script)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/x-shellscript")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Contains(t, script, "builder-7")
	assert.NotContains(t, script, "someone-else")
	assert.Contains(t, script, srv.URL+"/mcp", "it points at the address the server was reached on")
	assert.NotContains(t, script, myToken)
	// No env-file line of any value: the updater's own source mentions the
	// variable, so this looks for an assignment at the start of a line.
	assert.NotRegexp(t, `(?m)^SLOP_HAUS_TOKEN=`, script)
}

// Only an agent can ask, and only for itself -- there is no agent id in the
// request for a caller to change. A disabled agent sits alongside an
// unknown token here: Service.Authenticate tells the two apart internally
// (ErrBadToken vs. ErrIsDisabled, for logging), but both are the same 401
// on the wire, so both belong in this table.
func TestUpdateScript_RequiresAnAgentToken(t *testing.T) {
	srv, store, tokens := newAuthedServer(t)
	setUpWorkspace(t, srv, "builder-1")

	disabledID := agentIDByName(t, store, "builder-1")
	_, err := store.SetDisabled(context.Background(), disabledID, true)
	require.NoError(t, err)
	disabledToken := tokens.issue(store.tenantID, disabledID)

	for name, token := range map[string]string{
		"no token":       "",
		"unknown token":  "not-a-real-token",
		"disabled agent": disabledToken,
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := getUpdate(t, srv, token)

			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "body: %s", body)
			assert.NotContains(t, body, "slop-haus workspace update")
		})
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(body)
}

// fakeManifest hands back what the test gives it.
type fakeManifest struct {
	repos []workspace.Repo
	err   error
}

func (f fakeManifest) ScriptRepos(context.Context) ([]workspace.Repo, error) { return f.repos, f.err }

// The manifest reaches the script as quoted arguments to the repo syncer,
// and a manifest that cannot be read costs an update its cloning, not the
// update.
func TestUpdateScript_CarriesTheManifestToTheRepoSyncer(t *testing.T) {
	repos := []workspace.Repo{{Path: "app", Remote: "github.com/acme/app"}}
	srv, store, tokens := newAuthedServerWithManifest(t, fakeManifest{repos: repos})
	setUpWorkspace(t, srv, "builder-1")
	token := tokens.issue(store.tenantID, agentIDByName(t, store, "builder-1"))

	_, script := getUpdate(t, srv, token)
	assert.Contains(t, script, `"$workspace"/.slop-haus/sync-repos.sh -- 'app' 'github.com/acme/app' ||`)

	srv, store, tokens = newAuthedServerWithManifest(t, fakeManifest{err: errors.New("db down")})
	setUpWorkspace(t, srv, "builder-1")
	token = tokens.issue(store.tenantID, agentIDByName(t, store, "builder-1"))
	resp, script := getUpdate(t, srv, token)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, script, `sync-repos.sh --  ||`)
	assert.NotContains(t, script, "'github.com/acme/app'")
}

// The same manifest travels as JSON in a header, so a reader of the update does
// not have to parse the shell it is embedded in.
func TestUpdateScript_ListsTheManifestReposAsJSON(t *testing.T) {
	repos := []workspace.Repo{{Path: "app", Remote: "github.com/acme/app"}, {Path: "ü", Remote: "github.com/acme/it's"}}
	srv, store, tokens := newAuthedServerWithManifest(t, fakeManifest{repos: repos})
	setUpWorkspace(t, srv, "builder-1")
	token := tokens.issue(store.tenantID, agentIDByName(t, store, "builder-1"))

	resp, _ := getUpdate(t, srv, token)
	assert.JSONEq(t, `[{"path":"app","remote":"github.com/acme/app"},{"path":"ü","remote":"github.com/acme/it's"}]`, resp.Header.Get(agent.ReposHeader))
	assert.NotContains(t, resp.Header.Get(agent.ReposHeader), "ü", "ASCII on the wire")

	srv, store, tokens = newAuthedServerWithManifest(t, fakeManifest{})
	setUpWorkspace(t, srv, "builder-1")
	token = tokens.issue(store.tenantID, agentIDByName(t, store, "builder-1"))
	resp, _ = getUpdate(t, srv, token)
	assert.Equal(t, "[]", resp.Header.Get(agent.ReposHeader))
}

// fakeFleet hands back what the test gives it.
type fakeFleet struct {
	version *fleetinstructions.Version
	err     error
}

func (f *fakeFleet) Default(context.Context) (*fleetinstructions.Version, error) {
	return f.version, f.err
}

func (f *fakeFleet) DefaultOf(ctx context.Context, kind fleetinstructions.Kind) (*fleetinstructions.Version, error) {
	if kind == fleetinstructions.KindFleet {
		return f.Default(ctx)
	}
	return nil, f.err
}

func (f *fakeFleet) Get(context.Context, uuid.UUID) (*fleetinstructions.Version, error) {
	return nil, errors.New("fakeFleet: Get is covered against the real store")
}

// The default fleet instructions reach the briefing in the setup script and
// in the update. One that cannot be read fails the request: an update that
// went ahead without it would strip the text from every briefing it rewrites.
func TestScripts_CarryTheFleetInstructionsOrFailWithoutThem(t *testing.T) {
	fleet := &fakeFleet{version: &fleetinstructions.Version{Name: "house", Version: 4, Body: "Open draft PRs."}}
	srv, store, tokens := newAuthedServerWith(t, nil, fleet)

	dir := setUpWorkspace(t, srv, "builder-1")
	assert.Contains(t, readFile(t, filepath.Join(dir, workspace.HeadlessBriefing)), "> Open draft PRs.\n")

	token := tokens.issue(store.tenantID, agentIDByName(t, store, "builder-1"))
	resp, script := getUpdate(t, srv, token)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, script, `"house", version 4`)

	srv, store, tokens = newAuthedServerWith(t, nil, &fakeFleet{})
	setUpWorkspace(t, srv, "builder-1")
	token = tokens.issue(store.tenantID, agentIDByName(t, store, "builder-1"))
	_, script = getUpdate(t, srv, token)
	assert.NotContains(t, script, "Fleet instructions", "none chosen: no section")

	broken := &fakeFleet{}
	srv, store, tokens = newAuthedServerWith(t, nil, broken)
	setUpWorkspace(t, srv, "builder-1")
	token = tokens.issue(store.tenantID, agentIDByName(t, store, "builder-1"))
	broken.err = errors.New("db down")
	resp, _ = getUpdate(t, srv, token)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode, "the update")

	// Before the agent is created, so a failure leaves no ghost behind.
	resp, err := http.Post(srv.URL+"/api/agents/setup_script", "application/json", strings.NewReader(`{"name": "second"}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode, "the setup script")
	assert.NotContains(t, store.byName, "second")
}

// changeToolsLanding is in the briefing's landing rules only when they send
// agents to the change tools. A tool's name would not tell: the Remote Control
// host every script carries names all of slop-haus's tools.
const changeToolsLanding = "has a code host credential connected"

// ownToolsLanding is in them only when agents land changes with the repo's
// own tool, so a briefing without the change tools is shown to say what to
// do instead, not just to leave them out.
const ownToolsLanding = "The repo's CLAUDE.md says"

type fakeCodeHost struct {
	connected bool
	err       error
}

func (f *fakeCodeHost) Connected(context.Context) (bool, error) { return f.connected, f.err }

// A tenant with a code host credential gets briefings that send agents to the
// change tools, in the setup script and in the update; one without keeps the
// repo's own way. A credential that cannot be read fails the request rather
// than quietly sending the fleet back to the host's CLI.
func TestScripts_BriefingsFollowTheTenantsCodeHostCredential(t *testing.T) {
	withHost := func(h *fakeCodeHost) func(*agent.HandlerDeps) {
		return func(d *agent.HandlerDeps) { d.CodeHost = h }
	}

	connected := &fakeCodeHost{connected: true}
	srv, store, tokens := newAuthedServerWith(t, nil, nil, withHost(connected))
	dir := setUpWorkspace(t, srv, "builder-1")
	assert.Contains(t, readFile(t, filepath.Join(dir, workspace.HeadlessBriefing)), changeToolsLanding)
	token := tokens.issue(store.tenantID, agentIDByName(t, store, "builder-1"))
	_, script := getUpdate(t, srv, token)
	assert.Contains(t, script, changeToolsLanding)

	srv, store, tokens = newAuthedServerWith(t, nil, nil, withHost(&fakeCodeHost{}))
	dir = setUpWorkspace(t, srv, "builder-1")
	assert.NotContains(t, readFile(t, filepath.Join(dir, workspace.HeadlessBriefing)), changeToolsLanding)
	assert.Contains(t, readFile(t, filepath.Join(dir, workspace.HeadlessBriefing)), ownToolsLanding)
	token = tokens.issue(store.tenantID, agentIDByName(t, store, "builder-1"))
	_, script = getUpdate(t, srv, token)
	assert.NotContains(t, script, changeToolsLanding)
	assert.Contains(t, script, ownToolsLanding)

	broken := &fakeCodeHost{}
	srv, store, tokens = newAuthedServerWith(t, nil, nil, withHost(broken))
	setUpWorkspace(t, srv, "builder-1")
	token = tokens.issue(store.tenantID, agentIDByName(t, store, "builder-1"))
	broken.err = errors.New("db down")
	resp, _ := getUpdate(t, srv, token)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode, "the update")

	// Before the agent is created, so a failure leaves no ghost behind.
	resp, err := http.Post(srv.URL+"/api/agents/setup_script", "application/json", strings.NewReader(`{"name": "second"}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode, "the setup script")
	assert.NotContains(t, store.byName, "second")
}

func TestUpdateScript_NestedRootProtocolFollowsTheCallersKind(t *testing.T) {
	manifest := fakeManifest{repos: []workspace.Repo{{Path: "app", Remote: "github.com/acme/app"}}}
	for _, tc := range []struct {
		kind     agent.Kind
		contains string
		absent   string
	}{
		{agent.KindWorker, "This session is a headless run", "This is a Remote Control session"},
		// The headless briefing file is in every update, so only the interactive text is a signal for them.
		{agent.KindConductor, "This is a Remote Control session", ""},
		{agent.KindConsole, "the person's own interactive session", ""},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			srv, store, tokens := newAuthedServerWithManifest(t, manifest)
			a, err := store.Create(context.Background(), uuid.New(), "a-"+string(tc.kind), tc.kind)
			require.NoError(t, err)

			resp, body := getUpdate(t, srv, tokens.issue(a.Tenant, a.ID))

			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			assert.Contains(t, body, tc.contains)
			if tc.absent != "" {
				assert.NotContains(t, body, tc.absent)
			}
		})
	}
}
