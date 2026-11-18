package agent_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// The setup script is the whole onboarding path for anyone without a
// slop-haus checkout, so these drive the endpoint the way a browser does
// and then run what comes back.

func fetchScript(t *testing.T, mux *http.ServeMux, name string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/agents/setup_script",
		strings.NewReader(`{"name": "`+name+`"}`))
	req.Header.Set("Content-Type", "application/json")
	// httptest.NewRequest puts the URL's host on Request.Host, which is
	// where Go keeps it -- setting a "Host" header would do nothing.
	req.Host = "slop-haus.example"
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSetupScript_RegistersTheAgentAndReturnsARunnableScript(t *testing.T) {
	mux, _, _ := newTestMux()

	rec := fetchScript(t, mux, "builder-9", map[string]string{"X-Forwarded-Proto": "https"})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	assert.Contains(t, rec.Header().Get("Content-Type"), "text/x-shellscript")
	assert.Contains(t, rec.Header().Get("Content-Disposition"), "slop-haus-setup-builder-9.sh")
	// Per-request content (the agent it registered); nothing to share.
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	script := rec.Body.String()
	assert.Contains(t, script, "https://slop-haus.example/mcp",
		"the script points agents at the address this server was reached on")
	// The script carries no credential of its own -- an agent is
	// authorized through OAuth's device flow, not a bearer token baked in
	// here -- so the old token line is gone rather than merely empty.
	assert.NotRegexp(t, `(?m)^SLOP_HAUS_TOKEN=`, script,
		"setup must not embed a working credential in the script")
	assert.Contains(t, script, "token.sh login",
		"setup must walk the agent through OAuth's device flow instead")

	// And it runs: this is the artefact a human is told to execute in
	// their repo, so reading it is not enough.
	dir := t.TempDir()
	path := filepath.Join(dir, "setup.sh")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))

	cmd := exec.Command("bash", path)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "the script failed:\n%s", out)

	for _, rel := range []string{
		workspace.EnvFile, workspace.MCPConfig, workspace.Settings,
		workspace.LocalSettings, workspace.HeadlessBriefing, workspace.HookScript, workspace.ClaudeMD,
	} {
		_, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		assert.NoError(t, statErr, "%s should have been written", rel)
	}
}

// The token is minted as the script is rendered because it must be: only
// its hash is stored, so there is no second chance to put it in a file.
func TestSetupScript_MintsANewAgentEachTime(t *testing.T) {
	mux, store, _ := newTestMux()

	require.Equal(t, http.StatusCreated, fetchScript(t, mux, "first", nil).Code)
	require.Equal(t, http.StatusCreated, fetchScript(t, mux, "second", nil).Code)

	assert.Len(t, store.byID, 2)
}

// Names are unique, so a re-run for the same name is refused rather than
// quietly minting a second credential for one workspace. Uniqueness itself
// is the database's (see service_test.go); this pins the refusal's status.
func TestSetupScript_RefusesANameAlreadyTaken(t *testing.T) {
	mux, store, _ := newTestMux()
	store.createErr = fmt.Errorf("%w: %q", agent.ErrNameTaken, "builder-1")

	rec := fetchScript(t, mux, "builder-1", nil)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
}

func TestSetupScript_EmptyName_Returns422(t *testing.T) {
	mux, _, _ := newTestMux()

	rec := fetchScript(t, mux, "", nil)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
}

// Where the server thinks it is, is where it was reached. There is no
// configured URL to get wrong, and no default to be confidently wrong
// with -- http://localhost:8190 pastes cleanly on a deployed UI and
// points every agent at whoever ran it.
func TestSetupScript_TakesTheAddressFromTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		host    string
		proto   string
		expects string
	}{
		{
			name:    "behind a tls proxy",
			host:    "slop-haus.example",
			proto:   "https",
			expects: "https://slop-haus.example/mcp",
		},
		{
			name:    "the port is part of the address",
			host:    "slop-haus.example:8443",
			proto:   "https",
			expects: "https://slop-haus.example:8443/mcp",
		},
		{
			// nginx's $host drops the port; $http_host does not. That
			// distinction produced a script configuring agents to talk to
			// "http://localhost", which connects to nothing.
			name:    "the dev stack, reached on loopback with a port",
			host:    "localhost:5273",
			expects: "http://localhost:5273/mcp",
		},
		{
			// Nothing in front of us and not loopback: assume the
			// careful thing rather than the convenient one.
			name:    "reached directly on a hostname",
			host:    "slop-haus.example",
			expects: "https://slop-haus.example/mcp",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, _, _ := newTestMux()
			headers := map[string]string{"Host": tc.host}
			if tc.proto != "" {
				headers["X-Forwarded-Proto"] = tc.proto
			}

			rec := fetchScript(t, mux, "agent-"+strings.ReplaceAll(tc.name, " ", "-"), headers)
			require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.expects)
		})
	}
}

// The Host header is not ours, and it ends up in a config agents
// authenticate against. Anything that is not a host is refused rather
// than written into one.
func TestSetupScript_RefusesAnUnusableHost(t *testing.T) {
	for _, host := range []string{
		"slop-haus.example/../evil",
		"slop haus",
		`slop-haus"`,
	} {
		t.Run(host, func(t *testing.T) {
			mux, _, _ := newTestMux()

			rec := fetchScript(t, mux, "agent-x", map[string]string{"Host": host})

			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

// The filename is a download's name; an agent name is not ours to trust
// with one.
func TestSetupScript_FilenameCannotBeChosenByTheAgentName(t *testing.T) {
	mux, _, _ := newTestMux()

	rec := fetchScript(t, mux, "../../etc/profile.d/evil", nil)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	disposition := rec.Header().Get("Content-Disposition")
	assert.NotContains(t, disposition, "..")
	assert.NotContains(t, disposition, "/")
}

func rotateScript(mux *http.ServeMux, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/agents/"+id+"/setup_script", nil)
	req.Host = "slop-haus.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// A new script for an existing agent registers nothing, and signs out
// every workspace acting as that agent -- the script has no credential of
// its own to rotate, so the sign-out is the rotation.
func TestRotateSetupScript_SignsTheAgentOutAndReturnsItsScript(t *testing.T) {
	mux, store, _, grants := newTestMuxWithGrants()
	existing, err := store.Create(context.Background(), uuid.New(), "builder-3", agent.KindWorker)
	require.NoError(t, err)

	rec := rotateScript(mux, existing.ID.String())
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	assert.Contains(t, rec.Header().Get("Content-Type"), "text/x-shellscript")
	assert.Contains(t, rec.Header().Get("Content-Disposition"), "slop-haus-setup-builder-3.sh")
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Contains(t, rec.Body.String(), "https://slop-haus.example/mcp")
	assert.Contains(t, rec.Body.String(), "token.sh login")
	assert.Equal(t, []uuid.UUID{existing.ID}, grants.revoked)
	assert.Len(t, store.byID, 1, "no second agent was registered")
}

func TestRotateSetupScript_RefusesWithoutSigningOut(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     func(t *testing.T, store *fakeAgentStore) string
		status int
	}{
		{
			name:   "unknown agent",
			id:     func(*testing.T, *fakeAgentStore) string { return uuid.New().String() },
			status: http.StatusNotFound,
		},
		{
			name:   "malformed id",
			id:     func(*testing.T, *fakeAgentStore) string { return "not-a-uuid" },
			status: http.StatusUnprocessableEntity,
		},
		{
			// Its new sign-in would be refused (Service.Choose), so the
			// script would fail at login after the old workspace was cut off.
			name: "disabled agent",
			id: func(t *testing.T, store *fakeAgentStore) string {
				created, err := store.Create(context.Background(), uuid.New(), "off", agent.KindWorker)
				require.NoError(t, err)
				_, err = store.SetDisabled(context.Background(), created.ID, true)
				require.NoError(t, err)
				return created.ID.String()
			},
			status: http.StatusConflict,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, store, _, grants := newTestMuxWithGrants()

			rec := rotateScript(mux, tc.id(t, store))

			assert.Equal(t, tc.status, rec.Code, "body: %s", rec.Body.String())
			assert.Empty(t, grants.revoked)
		})
	}
}
