package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// planted stands in for a password, token or key: long enough for
// every minLength it meets, and unlike anything the server would say itself.
const planted = "planted-in-the-request"

// secretRoute is an operation whose body carries a secret.
type secretRoute struct {
	method, path string
	// as is who calls it: "" (no one), "user" or "supervisor".
	as string
	// fields is a body that type-checks; secrets names the fields in it
	// that hold a secret.
	fields  map[string]any
	secrets []string
}

// secretRoutes is every route whose body carries a credential. A new one
// belongs here.
var secretRoutes = []secretRoute{
	{method: http.MethodPost, path: "/api/auth/register", fields: map[string]any{"email": "a@example.com", "password": "", "display_name": "A"}, secrets: []string{"password"}},
	{method: http.MethodPost, path: "/api/auth/activate", fields: map[string]any{"token": ""}, secrets: []string{"token"}},
	{method: http.MethodPost, path: "/api/auth/login", fields: map[string]any{"email": "a@example.com", "password": ""}, secrets: []string{"password"}},
	{method: http.MethodPost, path: "/api/auth/password-reset/confirm", fields: map[string]any{"token": "", "password": ""}, secrets: []string{"token", "password"}},
	{method: http.MethodPost, path: "/api/auth/invite/preview", fields: map[string]any{"token": ""}, secrets: []string{"token"}},
	{method: http.MethodPost, path: "/api/auth/invite/accept", fields: map[string]any{"token": "", "password": "", "display_name": "A"}, secrets: []string{"token", "password"}},
	{method: http.MethodPost, path: "/api/auth/invite/accept/github", fields: map[string]any{"token": ""}, secrets: []string{"token"}},
	{method: http.MethodPost, path: "/api/invites/accept", as: "user", fields: map[string]any{"token": ""}, secrets: []string{"token"}},
	{method: http.MethodPost, path: "/api/auth/reauth/password", as: "user", fields: map[string]any{"password": ""}, secrets: []string{"password"}},
	{method: http.MethodPut, path: "/api/auth/sign-in-methods/password", as: "user", fields: map[string]any{"new_password": ""}, secrets: []string{"new_password"}},
	{method: http.MethodPut, path: "/api/codehost/github-app", as: "user", fields: map[string]any{"app_id": 1, "installation_id": 1, "private_key": ""}, secrets: []string{"private_key"}},
	// The code is exchanged with GitHub for the new App's key and webhook secret.
	{method: http.MethodPost, path: "/api/codehost/github-app/manifest/complete", as: "user", fields: map[string]any{"code": "", "state": ""}, secrets: []string{"code", "state"}},
	{method: http.MethodPost, path: "/api/codehost/github-app/install/complete", as: "user", fields: map[string]any{"installation_id": 1, "state": ""}, secrets: []string{"state"}},
	{method: http.MethodPost, path: "/api/oauth/device/lookup", as: "user", fields: map[string]any{"user_code": ""}, secrets: []string{"user_code"}},
	{method: http.MethodPost, path: "/api/oauth/device/decision", as: "user", fields: map[string]any{"user_code": "", "approve": false}, secrets: []string{"user_code"}},
	{method: http.MethodPost, path: "/api/supervisor/steward/credential/revoke", as: "supervisor", fields: map[string]any{"refresh_token": ""}, secrets: []string{"refresh_token"}},
	{method: http.MethodPost, path: "/api/supervisor/remote-control/rc-1/credential/revoke", as: "supervisor", fields: map[string]any{"refresh_token": ""}, secrets: []string{"refresh_token"}},
}

// credentialField is a body field named the way this API names a secret.
var credentialField = regexp.MustCompile(`^(password|new_password|token|refresh_token|access_token|secret|.*_secret|private_key|.*_key|key|user_code|code|state)$`)

// notSecret are fields credentialField matches that hold no secret.
var notSecret = map[string]bool{
	"POST /hooks/usage transcript_key": true, // which transcript, not a credential
}

// secretRoutes is written by hand, so a route added with a password or a
// token in its body is caught here and not left out of the test above.
func TestSecretRoutesListEveryBodyThatHoldsACredential(t *testing.T) {
	spec := apiserver.BuildOpenAPI()
	isListed := func(method, path, field string) bool {
		return slices.ContainsFunc(secretRoutes, func(r secretRoute) bool {
			return r.method == method && pathMatches(path, r.path) && slices.Contains(r.secrets, field)
		})
	}
	for path, item := range spec.Paths {
		for method, op := range map[string]*huma.Operation{http.MethodPost: item.Post, http.MethodPut: item.Put, http.MethodPatch: item.Patch, http.MethodDelete: item.Delete} {
			if op == nil || op.RequestBody == nil || op.RequestBody.Content["application/json"] == nil {
				continue
			}
			schema := op.RequestBody.Content["application/json"].Schema
			if schema.Ref != "" {
				schema = spec.Components.Schemas.SchemaFromRef(schema.Ref)
			}
			for field := range schema.Properties {
				key := method + " " + path + " " + field
				if !credentialField.MatchString(field) || notSecret[key] {
					continue
				}
				assert.True(t, isListed(method, path, field), "%s: add it to secretRoutes, or to notSecret if it holds no secret", key)
			}
		}
	}
}

// pathMatches compares a spec path, which has {params}, with a listed one,
// which has values in their place.
func pathMatches(spec, listed string) bool {
	a, b := strings.Split(spec, "/"), strings.Split(listed, "/")
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] && !(strings.HasPrefix(a[i], "{") && strings.HasSuffix(a[i], "}")) {
			return false
		}
	}
	return true
}

// malformedBodies are the ways a body that holds secret can fail validation,
// each of which huma's own errors quote back: a value too long, an object
// with a property it does not have, a value of the wrong type, and JSON
// that does not parse.
func malformedBodies(t *testing.T, r *secretRoute) map[string]string {
	t.Helper()
	with := func(secret any, extra map[string]any) string {
		body := map[string]any{}
		for k, v := range r.fields {
			body[k] = v
		}
		for _, k := range r.secrets {
			body[k] = secret
		}
		for k, v := range extra {
			body[k] = v
		}
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		return string(raw)
	}
	whole := with(planted, nil)
	return map[string]string{
		"too long":         with(planted+strings.Repeat("x", 4096), nil),
		"unknown property": with(planted, map[string]any{"unexpected": 1}),
		"wrong type":       with(map[string]string{"v": planted}, nil),
		"missing a field":  `{"` + r.secrets[0] + `":"` + planted + `","unexpected":1}`,
		"truncated":        whole[:len(whole)-2],
	}
}

// A body that fails validation is answered without what it held, and
// nothing logs it: the person who sent it may be the only one to see the
// answer, but proxies, browser tooling and error reports see it too.
func TestServe_ValidationErrorsNeverEchoASecret(t *testing.T) {
	t.Parallel()
	logs := &capturedLogs{}
	b := boot(t, &bootOptions{logs: slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}), intake: true})
	who, userToken := b.signedInUser(t)
	capOne := 1
	tokens := map[string]string{
		"user":       userToken,
		"supervisor": b.agentTokenFor(t, who, &oauth.Decision{Approve: true, AgentName: "box-1", Kind: "supervisor", SlotCap: &capOne}),
	}

	for i := range secretRoutes {
		r := &secretRoutes[i]
		for shape, body := range malformedBodies(t, r) {
			resp := do(t, &request{method: r.method, url: b.base + r.path, token: tokens[r.as], body: body,
				header: map[string]string{"Origin": b.base}})
			name := r.method + " " + r.path + ": " + shape
			// Refused by validation, not by anything before it: a route that
			// answered 401 would pass without showing anything. A secret with
			// no length limit (a key, a refresh token) is valid however long.
			if shape != "too long" {
				assert.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, resp.code, "%s: %s", name, resp.body)
			}
			assert.NotContains(t, resp.body, planted, name)
		}
	}

	t.Run("the OAuth token endpoint", func(t *testing.T) {
		for name, form := range map[string]string{
			"refresh":  url.Values{"grant_type": {"refresh_token"}, "client_id": {"nobody"}, "refresh_token": {planted}}.Encode(),
			"code":     url.Values{"grant_type": {"authorization_code"}, "client_id": {"nobody"}, "code": {planted}, "code_verifier": {planted}}.Encode(),
			"bad form": "grant_type=refresh_token&refresh_token=" + planted + "%zz",
		} {
			resp := do(t, &request{method: http.MethodPost, url: b.base + "/oauth/token", body: form,
				header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}})
			assert.GreaterOrEqual(t, resp.code, 400, name)
			assert.NotContains(t, resp.body, planted, name)
		}
	})

	// The intake door's credential is a header; the refusal is the same
	// fixed text for every way it fails.
	t.Run("the intake door", func(t *testing.T) {
		resp := do(t, &request{method: http.MethodPost, url: "http://" + b.cfg.Intake.ListenAddr + "/hooks/intake/" + uuid.NewString(),
			body: `{"key":"` + planted + `"`, header: map[string]string{
				"Authorization": "Bearer " + planted, intake.SignatureHeader: "sha256=" + planted, "Content-Type": "application/json",
			}})
		assert.Equal(t, http.StatusUnauthorized, resp.code)
		assert.NotContains(t, resp.body, planted)
	})

	assert.NotContains(t, logs.String(), planted)
}

// capturedLogs is an io.Writer the server's log handler writes to from
// every request's goroutine.
type capturedLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *capturedLogs) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *capturedLogs) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}
