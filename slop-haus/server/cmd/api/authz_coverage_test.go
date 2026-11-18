package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/auth"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// The authorisation coverage test calls every route the API serves as
// another tenant's user and as a plain member, so the bug class that
// matters most on a multi-tenant board -- one tenant reaching another's
// data, a member doing an owner's action -- cannot ship on a route nobody
// thought to test. The routes come from the OpenAPI spec the generated
// client is built from; a route with no case in testdata/authz/cases.json
// and no entry in its allowlist fails TestEveryRouteHasAnAuthzCase, naming
// the route.
//
// To cover a new route, add a case for its operationId to cases.json:
//
//	{"operation": "getThing", "params": {"id": "@ticket"}, "foreign": true}
//
//   - params fills the path's {placeholders}; "@name" is the id of tenant
//     A's fixture of that name (see authzFixtures), anything else is literal.
//   - query is appended after "?".
//   - body is the JSON request body; any string "@name" in it is replaced by
//     the fixture's id (a number or a string, as the fixture has it). It
//     must pass validation, or the call never reaches the authorisation
//     under test (the test fails a case answered 400 or 422).
//   - foreign says the call names one of A's objects, so another tenant's
//     owner must be answered 403 or 404. Leave it out for a route that acts
//     on the caller's own tenant (a listing, say): another tenant's owner
//     then only has to see none of A's data.
//   - owner_only says a plain member must be answered 403. The routes do not
//     declare this (their services check the caller's role), so the table
//     does, and pins it both ways: a route that stops being owner-only
//     fails too.
//
// Routes that are deliberately open to anyone are listed under "allow" with
// the reason. They are called without a session (writes naming the site as
// Origin, as the page does) with the allow entry's params, query and body, and
// must answer something other than 401 or 403, or one of "expect". An empty
// body is enough for most: the route is reached, though not exercised.
//
// Fixtures that need more than a route to make (a proposal, which only an agent
// files) are made in makeFixtures. The event streams and the steward's tools
// have tests of their own below: a stream has to be read, and the steward is
// off for the tenants of the table's world.

// Read at run time rather than embedded: own_tests.sh runs a changed test
// file against the merge-base tree, which would not compile with an embed
// of a file it lacks.
const authzCasesPath = "testdata/authz/cases.json"

type authzCase struct {
	Operation string         `json:"operation"`
	Params    map[string]any `json:"params"`
	Query     string         `json:"query"`
	Body      any            `json:"body"`
	OwnerOnly bool           `json:"owner_only"`
	Foreign   bool           `json:"foreign"`
	// Accept are further statuses that count as the route refusing the call,
	// where it answers before it reaches the authorisation under test (a
	// feature the test api is not booted with, a precondition B does not
	// meet). Note says why.
	Accept []int  `json:"accept"`
	Note   string `json:"note"`
}

// authzAllow is a route open to anyone. It is called without a session, with
// the request a case has; it must answer something other than 401 and 403
// (Expect names the statuses it answers instead, where it does) and hold
// nothing of A's.
type authzAllow struct {
	Operation string         `json:"operation"`
	Reason    string         `json:"reason"`
	Params    map[string]any `json:"params"`
	Query     string         `json:"query"`
	Body      any            `json:"body"`
	Expect    []int          `json:"expect"`
}

type authzTable struct {
	Cases []authzCase  `json:"cases"`
	Allow []authzAllow `json:"allow"`
}

func loadAuthzTable(t *testing.T) *authzTable {
	t.Helper()
	var table authzTable
	raw, err := os.ReadFile(authzCasesPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &table))
	return &table
}

// authzRoute is one operation of the served spec.
type authzRoute struct {
	op     *huma.Operation
	method string
	path   string
	marker string
}

func (r authzRoute) String() string {
	return fmt.Sprintf("%s %s (%s)", r.method, r.path, r.op.OperationID)
}

func authzRoutes() []authzRoute {
	var routes []authzRoute
	for path, item := range apiserver.BuildOpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{
			http.MethodGet: item.Get, http.MethodPost: item.Post, http.MethodPut: item.Put,
			http.MethodPatch: item.Patch, http.MethodDelete: item.Delete,
		} {
			if op != nil {
				routes = append(routes, authzRoute{op: op, method: method, path: path, marker: authn.MarkerName(op)})
			}
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].op.OperationID < routes[j].op.OperationID })
	return routes
}

// TestEveryRouteHasAnAuthzCase is the coverage gate. It needs no database.
func TestEveryRouteHasAnAuthzCase(t *testing.T) {
	t.Parallel()
	table := loadAuthzTable(t)
	cases, allowed := map[string]bool{}, map[string]bool{}
	for _, c := range table.Cases {
		assert.False(t, cases[c.Operation], "%s has two cases", c.Operation)
		if len(c.Accept) > 0 {
			assert.NotEmpty(t, c.Note, "%s accepts other statuses without saying why", c.Operation)
		}
		cases[c.Operation] = true
	}
	for _, a := range table.Allow {
		assert.NotEmpty(t, a.Reason, "%s is allowlisted without a reason", a.Operation)
		assert.False(t, cases[a.Operation], "%s is both a case and allowlisted", a.Operation)
		allowed[a.Operation] = true
	}
	known := map[string]bool{}
	for _, r := range authzRoutes() {
		known[r.op.OperationID] = true
		if !cases[r.op.OperationID] && !allowed[r.op.OperationID] {
			t.Errorf("%s has no authorisation case. Add one for %q to cmd/api/testdata/authz/cases.json "+
				"(how: the comment at the top of cmd/api/authz_coverage_test.go), or, if it is open to anyone "+
				"on purpose, to its \"allow\" list with the reason", r, r.op.OperationID)
		}
		if allowed[r.op.OperationID] {
			assert.Contains(t, []string{"public", "optional-user"}, r.marker,
				"%s is allowlisted but is not a public route: only those may be", r)
		}
	}
	for name := range cases {
		assert.True(t, known[name], "cases.json has a case for %q, which is not a route", name)
	}
	for name := range allowed {
		assert.True(t, known[name], "cases.json allows %q, which is not a route", name)
	}
}

// authzWorld is one running api with two tenants: A, whose objects are
// attacked, and B, whose people and agents attack them.
type authzWorld struct {
	t    *testing.T
	rig  *contractRig
	base string

	// secret is in every piece of text A's fixtures carry, so a response
	// that holds it has leaked an A object.
	secret string
	// leaks are the strings no response to B may hold.
	leaks []string

	tokens map[string]string
	// people are the signed-in callers, who get a session of their own per
	// call: some routes (a password change) sign the person out of the
	// sessions they had.
	people   map[string]*authn.User
	fixtures map[string]any
}

const (
	asAOwner  = "a_owner"
	asAMember = "a_member"
	asBOwner  = "b_owner"
	asBMember = "b_member"
	asAWorker = "a_worker"
	asBWorker = "b_worker"
	asASuper  = "a_supervisor"
	asBSuper  = "b_supervisor"
	asNobody  = ""
)

func newAuthzWorld(t *testing.T) *authzWorld {
	t.Helper()
	rig := newContractRigWith(t, &bootOptions{intake: true, replays: true})
	w := &authzWorld{t: t, rig: rig, base: rig.boot.base, secret: "authz-secret-" + uuid.NewString(),
		tokens: map[string]string{}, people: map[string]*authn.User{}, fixtures: map[string]any{}}
	b := rig.boot
	tokenFor := w.freshSession
	member := func(tenant uuid.UUID, role string) *authn.User {
		who := &authn.User{ID: dbtest.NewUser(t, b.appDB), TenantID: tenant}
		_, err := b.appDB.Global().Exec(t.Context(),
			`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, $4)`,
			uuid.New(), tenant, who.ID, role)
		require.NoError(t, err)
		return who
	}
	tenantB := dbtest.NewTenant(t, b.appDB)
	aMember, bOwner, bMember := member(rig.user.TenantID, "member"), member(tenantB, "owner"), member(tenantB, "member")
	// Routes that mint a new session check the person behind it is active;
	// people made by dbtest are not.
	_, err := b.appDB.Global().Exec(t.Context(),
		`UPDATE global_users SET is_active = true, sessions_valid_after = now() - interval '1 hour'`)
	require.NoError(t, err)
	w.people[asAOwner], w.people[asAMember], w.people[asBOwner], w.people[asBMember] = rig.user, aMember, bOwner, bMember
	for who := range w.people {
		w.tokens[who] = tokenFor(w.people[who])
	}

	agentToken := func(who *authn.User, kind string) string {
		d := &oauth.Decision{Approve: true, AgentName: kind + "-" + uuid.NewString()[:8], Kind: kind}
		if kind == "supervisor" {
			slots := 2
			d.SlotCap = &slots
		}
		return b.agentTokenFor(t, who, d)
	}
	ownerA := rig.user
	w.tokens[asAWorker], w.tokens[asBWorker] = agentToken(ownerA, "worker"), agentToken(bOwner, "worker")
	w.tokens[asASuper], w.tokens[asBSuper] = agentToken(ownerA, "supervisor"), agentToken(bOwner, "supervisor")

	w.fixtures["tenant"] = ownerA.TenantID.String()
	w.fixtures["member"] = aMember.ID.String()
	w.leaks = []string{w.secret, ownerA.TenantID.String(), aMember.ID.String(), ownerA.ID.String()}
	w.makeFixtures()
	// Anything of A's a response to B must not carry: every fixture's id
	// (the small numbers of tickets would match anywhere).
	for _, id := range w.fixtures {
		if s, ok := id.(string); ok {
			w.leaks = append(w.leaks, s)
		}
	}
	return w
}

// freshSession is a token for who in a session that has just signed in.
func (w *authzWorld) freshSession(who *authn.User) string {
	w.t.Helper()
	cfg := w.rig.boot.cfg
	sessionID, err := auth.NewSessionID()
	require.NoError(w.t, err)
	token, err := auth.NewSigner([]byte(bootJWTSecret), cfg.AccessTokenTTL, cfg.RefreshTokenTTL).
		IssueSessionAccessToken(who.ID, who.TenantID, &auth.Session{ID: sessionID, AuthTime: time.Now()})
	require.NoError(w.t, err)
	return token
}

// id reads a created object's "id".
func (w *authzWorld) id(resp *authzResponse) any {
	w.t.Helper()
	var obj map[string]any
	require.NoError(w.t, json.Unmarshal([]byte(resp.body), &obj), resp.body)
	if id, ok := obj["id"]; ok {
		return id
	}
	// Some answers wrap the object: {"source": {"id": ...}}.
	for _, v := range obj {
		if nested, ok := v.(map[string]any); ok && nested["id"] != nil {
			return nested["id"]
		}
	}
	require.Fail(w.t, "no id", resp.body)
	return nil
}

// makeFixtures creates, through the API as A's owner, one object of each
// kind a route can be pointed at. A fixture is a real row, so a missing
// tenant check shows as a 200, not as the 404 a made-up id would get anyway.
func (w *authzWorld) makeFixtures() {
	create := func(name, method, path string, body any) {
		who := asAOwner
		if name == "slot" {
			who = asASuper
		}
		resp := w.call(who, method, path, body)
		require.Contains(w.t, []int{http.StatusOK, http.StatusCreated}, resp.code, "fixture %s: %s %s: %s", name, method, path, resp.body)
		w.fixtures[name] = w.id(resp)
	}
	// Registering an agent or a conductor answers with its setup script, so
	// its id is read off the listing.
	setup := func(name, path, key, agentName string) {
		w.mustCall(asAOwner, http.StatusCreated, http.MethodPost, path, jsonObject{"name": agentName})
		var listed map[string][]struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		require.NoError(w.t, json.Unmarshal([]byte(w.mustCall(asAOwner, http.StatusOK, http.MethodGet, path, nil).body), &listed))
		for _, e := range listed[key] {
			if e.Name == agentName {
				w.fixtures[name] = e.ID
				return
			}
		}
		require.Failf(w.t, "fixture "+name, "%s lists no %q", path, agentName)
	}
	ticket := func(title string, path string) jsonObject {
		return jsonObject{"title": title + " " + w.secret, "body": w.secret, "status": "open", "paths": []jsonObject{{"path": path}}}
	}
	// Tickets are numbered per tenant from 1, so A's would be B's own ticket
	// of the same number. Numbering past anything B makes keeps A's out of
	// B's reach.
	for range 40 {
		w.mustCall(asAOwner, http.StatusCreated, http.MethodPost, "/api/tickets", jsonObject{"title": "filler", "status": "preparing"})
	}
	create("ticket", http.MethodPost, "/api/tickets", ticket("one", "authz/one/"))
	create("ticket2", http.MethodPost, "/api/tickets", ticket("two", "authz/two/"))
	create("role", http.MethodPost, "/api/agent-roles", jsonObject{"name": "role-" + w.secret})
	setup("conductor", "/api/conductors", "conductors", "conductor-"+w.secret)
	create("invite", http.MethodPost, "/api/invites", jsonObject{"email": w.secret + "@example.invalid"})
	create("fleet", http.MethodPost, "/api/fleet_instructions/versions", jsonObject{"name": "fleet-" + w.secret, "body": w.secret, "make_default": false})
	create("variant", http.MethodPost, "/api/variants/versions",
		jsonObject{"name": "variant-" + w.secret, "harness": "claude", "settings": jsonObject{}, "fleet_instructions_version_id": nil})
	create("evalset", http.MethodPost, "/api/eval_sets", jsonObject{"name": "evalset-" + w.secret, "description": "", "ticket_ids": []int64{}})
	create("intake_source", http.MethodPost, "/api/intake/sources",
		jsonObject{"name": "source-" + w.secret, "adapter": "native", "trust": "untrusted", "auth": "bearer"})
	setup("agent", "/api/agents", "agents", "agent-"+w.secret)
	w.mustCall(asAOwner, http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/tickets/%v/comments", w.fixtures["ticket"]), jsonObject{"body": w.secret})

	var grants struct {
		Grants []struct {
			ID string `json:"id"`
		} `json:"grants"`
	}
	list := w.mustCall(asAOwner, http.StatusOK, http.MethodGet, "/api/oauth/grants", nil)
	require.NoError(w.t, json.Unmarshal([]byte(list.body), &grants))
	require.NotEmpty(w.t, grants.Grants, "A's agents hold grants: %s", list.body)
	w.fixtures["grant"] = grants.Grants[0].ID

	var supervisors struct {
		Supervisors []struct {
			ID string `json:"id"`
		} `json:"supervisors"`
	}
	list = w.mustCall(asAOwner, http.StatusOK, http.MethodGet, "/api/supervisors", nil)
	require.NoError(w.t, json.Unmarshal([]byte(list.body), &supervisors))
	require.NotEmpty(w.t, supervisors.Supervisors, "A's supervisor is listed: %s", list.body)
	w.fixtures["supervisor"] = supervisors.Supervisors[0].ID

	w.mustCall(asAOwner, http.StatusOK, http.MethodPost, fmt.Sprintf("/api/tickets/%v/blockers", w.fixtures["ticket"]),
		jsonObject{"blocker_id": w.fixtures["ticket2"]})
	// The conversation's first message is the secret, so a stream or a
	// transcript read as another tenant would show it.
	create("conversation", http.MethodPost, fmt.Sprintf("/api/conductors/%v/conversations", w.fixtures["conductor"]), jsonObject{"message": w.secret})
	create("slot", http.MethodPost, "/api/supervisor/slots", jsonObject{"name": "slot-fixture"})
	w.fixtures["unmade"] = uuid.NewString()
	w.makeProposal()
}

// makeProposal has a console of A's propose opening a preparing ticket of
// A's: only an agent files a proposal, and not through a route.
func (w *authzWorld) makeProposal() {
	w.t.Helper()
	ticket := w.id(w.mustCall(asAOwner, http.StatusCreated, http.MethodPost, "/api/tickets",
		jsonObject{"title": "proposed " + w.secret, "body": w.secret, "status": "preparing"}))
	console := w.rig.console("authz-console")
	w.fixtures["proposal"] = proposalID(w.t, console.mustCall("propose_ready", jsonObject{"ticket_id": ticket}))
}

type authzResponse struct {
	code int
	body string
}

// call makes a request as who ("" is nobody). A stream is read for its
// status line only.
func (w *authzWorld) call(who, method, path string, body any) *authzResponse {
	w.t.Helper()
	return w.callWith(who, method, path, body, nil)
}

func (w *authzWorld) callWith(who, method, path string, body any, headers map[string]string) *authzResponse {
	w.t.Helper()
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(w.t, err)
		payload = strings.NewReader(string(b))
	}
	ctx, cancel := context.WithTimeout(w.t.Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, w.base+path, payload)
	require.NoError(w.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if person := w.people[who]; person != nil {
		w.tokens[who] = w.freshSession(person)
	}
	if token := w.tokens[who]; token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	require.NoError(w.t, err, "%s %s", method, path)
	defer func() { _ = resp.Body.Close() }()
	out := &authzResponse{code: resp.StatusCode}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return out
	}
	raw, err := io.ReadAll(resp.Body)
	require.NoError(w.t, err)
	out.body = string(raw)
	return out
}

func (w *authzWorld) mustCall(who string, code int, method, path string, body any) *authzResponse {
	w.t.Helper()
	resp := w.call(who, method, path, body)
	require.Equal(w.t, code, resp.code, "%s %s: %s", method, path, resp.body)
	return resp
}

// resolve replaces "@name" strings in v with the fixture's id.
func (w *authzWorld) resolve(v any) any {
	switch v := v.(type) {
	case string:
		if name, ok := strings.CutPrefix(v, "@"); ok {
			id, found := w.fixtures[name]
			require.True(w.t, found, "no fixture %q (see makeFixtures)", name)
			return id
		}
		return v
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = w.resolve(v[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = w.resolve(e)
		}
		return out
	default:
		return v
	}
}

// request is the case as a method, a path with a query, and a body.
func (w *authzWorld) request(r authzRoute, c *authzCase) (method, path string, body any) {
	w.t.Helper()
	path = r.path
	for name, v := range c.Params {
		placeholder := "{" + name + "}"
		require.Contains(w.t, path, placeholder, "%s: case names a parameter the path lacks", r)
		path = strings.ReplaceAll(path, placeholder, fmt.Sprint(w.resolve(v)))
	}
	require.NotContains(w.t, path, "{", "%s: case fills no value for every path parameter: %s", r, path)
	if c.Query != "" {
		path += "?" + c.Query
	}
	if c.Body != nil {
		body = w.resolve(c.Body)
	} else if r.method != http.MethodGet && r.method != http.MethodDelete {
		body = jsonObject{}
	}
	return r.method, path, body
}

// answered fails the test for an answer that says nothing about
// authorisation: a server error, or a request refused as malformed before
// anyone was asked whether the caller may.
func (w *authzWorld) answered(r authzRoute, c *authzCase, who string, resp *authzResponse) {
	w.t.Helper()
	if slices.Contains(c.Accept, resp.code) {
		return
	}
	assert.Less(w.t, resp.code, 500, "%s as %s: server error: %s", r, who, resp.body)
	assert.NotContains(w.t, []int{http.StatusUnauthorized, http.StatusBadRequest, http.StatusUnprocessableEntity}, resp.code,
		"%s as %s: the caller was not admitted or the case's request is invalid, so no authorisation was exercised -- fix the case: %s", r, who, resp.body)
}

func (w *authzWorld) noLeak(r authzRoute, who string, resp *authzResponse) {
	w.t.Helper()
	for _, leak := range w.leaks {
		assert.NotContains(w.t, resp.body, leak, "%s as %s: the answer holds tenant A's data", r, who)
	}
}

// TestAuthzCoverage runs every case against one api. Subtests share the
// fixtures, so order matters: they go from the calls that cannot change
// anything to the ones that can, and a member's, which succeed on routes
// that are open to members, last.
func TestAuthzCoverage(t *testing.T) {
	t.Parallel()
	table := loadAuthzTable(t)
	byOp := map[string]*authzCase{}
	for i := range table.Cases {
		byOp[table.Cases[i].Operation] = &table.Cases[i]
	}
	w := newAuthzWorld(t)

	type target struct {
		route authzRoute
		c     *authzCase
	}
	var user, agent, admin []target
	routeByOperation := map[string]authzRoute{}
	for _, r := range authzRoutes() {
		routeByOperation[r.op.OperationID] = r
		c := byOp[r.op.OperationID]
		if c == nil {
			continue // TestEveryRouteHasAnAuthzCase says which
		}
		switch r.marker {
		case "user":
			user = append(user, target{r, c})
		case "agent":
			agent = append(agent, target{r, c})
		case "admin":
			admin = append(admin, target{r, c})
		default:
			t.Errorf("%s: a %q route has a case; only user, agent and admin routes take one", r, r.marker)
		}
	}

	notSignedIn := func(t *testing.T, ts []target) {
		for _, tg := range ts {
			m, p, b := w.request(tg.route, tg.c)
			assert.Equal(t, http.StatusUnauthorized, w.call(asNobody, m, p, b).code, "%s unauthenticated", tg.route)
		}
	}
	t.Run("unauthenticated", func(t *testing.T) {
		notSignedIn(t, user)
		notSignedIn(t, agent)
		notSignedIn(t, admin)
	})

	// The calls of another tenant's owner, who can do anything in their own.
	t.Run("another tenant's owner", func(t *testing.T) {
		for _, tg := range user {
			m, p, b := w.request(tg.route, tg.c)
			resp := w.call(asBOwner, m, p, b)
			if tg.c.Foreign {
				assert.Contains(t, append([]int{http.StatusForbidden, http.StatusNotFound}, tg.c.Accept...), resp.code,
					"%s as tenant B's owner on A's object: %s", tg.route, resp.body)
			}
			w.answered(tg.route, tg.c, asBOwner, resp)
			w.noLeak(tg.route, asBOwner, resp)
		}
	})

	t.Run("agents", func(t *testing.T) {
		for _, tg := range agent {
			m, p, b := w.request(tg.route, tg.c)
			supervisorRoute := !authn.SupervisorAllowed(tg.route.op, false)
			workerRoute := !authn.SupervisorAllowed(tg.route.op, true)
			// A person is not an agent, nor an agent a person.
			assert.Equal(t, http.StatusUnauthorized, w.call(asAOwner, m, p, b).code, "%s as a signed-in person", tg.route)
			if supervisorRoute {
				assert.Equal(t, http.StatusForbidden, w.call(asAWorker, m, p, b).code, "%s as a worker", tg.route)
			}
			if workerRoute {
				assert.Equal(t, http.StatusForbidden, w.call(asASuper, m, p, b).code, "%s as a supervisor", tg.route)
			}
			other := asBWorker
			if supervisorRoute {
				other = asBSuper
			}
			resp := w.call(other, m, p, b)
			if tg.c.Foreign {
				assert.Contains(t, append([]int{http.StatusForbidden, http.StatusNotFound}, tg.c.Accept...), resp.code,
					"%s as tenant B's agent on A's object: %s", tg.route, resp.body)
			}
			// A route that is off in the test api (no object store) answers its accepted 503.
			assert.True(t, resp.code < 500 || slices.Contains(tg.c.Accept, resp.code), "%s as %s: %d %s", tg.route, other, resp.code, resp.body)
			w.noLeak(tg.route, other, resp)
		}
	})

	// Positive control: the rightful caller is not turned away, so a
	// blanket 401 or 403 cannot make every refusal above pass. A route may
	// still answer 404 or 409 for what an earlier call did to a fixture.
	t.Run("the rightful caller", func(t *testing.T) {
		for _, tg := range agent {
			m, p, b := w.request(tg.route, tg.c)
			who := asAWorker
			if !authn.SupervisorAllowed(tg.route.op, false) {
				who = asASuper
			}
			resp := w.call(who, m, p, b)
			// Not 401 only: a route may be for one kind of agent (a console's,
			// a conductor's host) and refuse the others with 403.
			assert.NotEqual(t, http.StatusUnauthorized, resp.code, "%s as tenant A's own agent: %s", tg.route, resp.body)
		}
	})

	t.Run("admin routes", func(t *testing.T) {
		for _, tg := range admin {
			m, p, b := w.request(tg.route, tg.c)
			for _, who := range []string{asAOwner, asAMember, asBOwner} {
				assert.Equal(t, http.StatusForbidden, w.call(who, m, p, b).code, "%s as %s, who is no admin", tg.route, who)
			}
		}
	})

	// Owner-only routes first: a member who is refused changes nothing,
	// whereas one let through a route open to members may delete a fixture.
	t.Run("a plain member", func(t *testing.T) {
		rank := func(tg target) int {
			switch {
			case tg.c.OwnerOnly:
				return 0
			case tg.route.method == http.MethodDelete:
				return 2
			}
			return 1
		}
		sort.SliceStable(user, func(i, j int) bool { return rank(user[i]) < rank(user[j]) })
		for _, tg := range user {
			m, p, b := w.request(tg.route, tg.c)
			resp := w.call(asAMember, m, p, b)
			if tg.c.OwnerOnly {
				assert.Equal(t, http.StatusForbidden, resp.code, "%s as a plain member: owner-only: %s", tg.route, resp.body)
				continue
			}
			assert.NotEqual(t, http.StatusForbidden, resp.code,
				"%s as a plain member: answered 403, so it is owner-only now; set owner_only in its case: %s", tg.route, resp.body)
			w.answered(tg.route, tg.c, asAMember, resp)
		}
	})

	t.Run("public routes, anonymously", func(t *testing.T) {
		for _, a := range table.Allow {
			r, ok := routeByOperation[a.Operation]
			if !ok {
				continue // TestEveryRouteHasAnAuthzCase says which
			}
			m, p, b := w.request(r, &authzCase{Params: a.Params, Query: a.Query, Body: a.Body})
			// A write is refused unless it names the site as the page does.
			headers := map[string]string{}
			if m != http.MethodGet {
				headers["Origin"] = w.base
			}
			resp := w.callWith(asNobody, m, p, b, headers)
			if len(a.Expect) > 0 {
				assert.Contains(t, a.Expect, resp.code, "%s anonymously: %s", r, resp.body)
			} else {
				assert.NotContains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, resp.code, "%s anonymously: %s", r, resp.body)
			}
			assert.Less(t, resp.code, 500, "%s anonymously: %s", r, resp.body)
			w.noLeak(r, asNobody, resp)
		}
	})

	t.Run("the owner", func(t *testing.T) {
		for _, tg := range user {
			m, p, b := w.request(tg.route, tg.c)
			resp := w.call(asAOwner, m, p, b)
			if slices.Contains(tg.c.Accept, resp.code) {
				continue
			}
			assert.NotContains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, resp.code, "%s as tenant A's own owner: %s", tg.route, resp.body)
		}
	})
}

// authzStream is an event stream read as a browser's EventSource does. A
// refused request has no events; its status and body are kept.
type authzStream struct {
	w      *authzWorld
	status int
	body   string
	events chan string
}

func (w *authzWorld) openStream(who, path string) *authzStream {
	w.t.Helper()
	ctx, cancel := context.WithCancel(w.t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.base+path, nil)
	require.NoError(w.t, err)
	if person := w.people[who]; person != nil {
		w.tokens[who] = w.freshSession(person)
	}
	req.Header.Set("Authorization", "Bearer "+w.tokens[who])
	resp, err := http.DefaultClient.Do(req)
	require.NoError(w.t, err)
	w.t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	s := &authzStream{w: w, status: resp.StatusCode, events: make(chan string, 256)}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw, err := io.ReadAll(resp.Body)
		require.NoError(w.t, err)
		s.body = string(raw)
		close(s.events)
		return s
	}
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var data []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case line == "" && len(data) > 0:
				select {
				case s.events <- strings.Join(data, "\n"):
				case <-ctx.Done():
					return
				}
				data = nil
			}
		}
	}()
	return s
}

// readUntil returns the events up to and including the first that holds
// want. The wait is bounded only so a stream that never delivers fails the
// test rather than hangs it.
func (s *authzStream) readUntil(want string) []string {
	s.w.t.Helper()
	var got []string
	timeout := time.After(30 * time.Second)
	for {
		select {
		case ev, ok := <-s.events:
			require.True(s.w.t, ok, "the stream ended before an event held %q; got %v", want, got)
			got = append(got, ev)
			if strings.Contains(ev, want) {
				return got
			}
		case <-timeout:
			require.FailNow(s.w.t, "no event held "+want, "got %v", got)
		}
	}
}

func (s *authzStream) first() string {
	s.w.t.Helper()
	return s.readUntil("")[0]
}

// The streams are only status-checked by TestAuthzCoverage; here they are
// read. A's own stream must carry A's change (or the test proves nothing),
// and B's, held open while A changes things, must carry none of it.
func TestAuthzStreamsKeepToTheirTenant(t *testing.T) {
	t.Parallel()
	w := newAuthzWorld(t)
	newTicket := func(who, title string) {
		w.mustCall(who, http.StatusCreated, http.MethodPost, "/api/tickets", jsonObject{"title": title, "status": "preparing"})
	}

	t.Run("the board", func(t *testing.T) {
		const path = "/api/tickets/stream"
		a, b := w.openStream(asAOwner, path), w.openStream(asBOwner, path)
		require.Equal(t, http.StatusOK, a.status, a.body)
		require.Equal(t, http.StatusOK, b.status, b.body)
		a.first()
		b.first()

		newTicket(asAOwner, "streamed "+w.secret)
		a.readUntil(w.secret)

		// B's own change is the proof its stream has polled since A's.
		own := "b-own-" + uuid.NewString()
		newTicket(asBOwner, own)
		for _, ev := range b.readUntil(own) {
			for _, leak := range w.leaks {
				assert.NotContains(t, ev, leak, "tenant B's board stream holds tenant A's data")
			}
		}
	})

	t.Run("a conversation", func(t *testing.T) {
		path := fmt.Sprintf("/api/conductors/%v/conversations/%v/stream", w.fixtures["conductor"], w.fixtures["conversation"])
		own := w.openStream(asAOwner, path)
		require.Equal(t, http.StatusOK, own.status, own.body)
		own.readUntil(w.secret)

		for _, who := range []string{asBOwner, asAMember} {
			refused := w.openStream(who, path)
			assert.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, refused.status, "%s: %s", who, refused.body)
			for _, leak := range w.leaks {
				assert.NotContains(t, refused.body, leak, "%s was answered with tenant A's data", who)
			}
		}
	})
}

// The test api's tenants have the steward off, so a steward of B's is
// refused by its tenant's setting before any proposal is looked up. With it
// at the assist level for both (drafting is refused below that, ahead of the
// proposal lookup too), B's steward must still be refused A's proposal, and
// A's own must not be turned away by the same refusal.
func TestAuthzStewardToolsKeepToTheirTenant(t *testing.T) {
	t.Parallel()
	w := newAuthzWorld(t)
	for _, owner := range []string{asAOwner, asBOwner} {
		w.mustCall(owner, http.StatusOK, http.MethodPatch, "/api/settings/steward_level", jsonObject{"type": "int", "int_val": 2})
	}
	stewardSession := func(supervisor string) *mcp.ClientSession {
		var tokens struct {
			AccessToken string `json:"access_token"`
		}
		issued := w.mustCall(supervisor, http.StatusOK, http.MethodPost, "/api/supervisor/steward/credential", nil)
		require.NoError(t, json.Unmarshal([]byte(issued.body), &tokens))
		session, err := mcp.NewClient(&mcp.Implementation{Name: "authz-steward", Version: "0.0.1"}, nil).Connect(t.Context(),
			&mcp.StreamableClientTransport{Endpoint: w.base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: tokens.AccessToken}}}, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = session.Close() })
		return session
	}
	callTool := func(s *mcp.ClientSession, tool string, args jsonObject) (res *mcp.CallToolResult, text string) {
		res, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
		require.NoError(t, err)
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		return res, text
	}
	call := func(s *mcp.ClientSession, tool, proposal string) (refused bool, text string) {
		res, text := callTool(s, tool, jsonObject{"proposal_id": proposal, "category": "ticket_ready", "reason": "routine"})
		return res.IsError, text
	}
	// Read as A: the table is row-level secured by tenant.
	count := func(table string) (n int) {
		ctx := db.WithTenant(t.Context(), w.rig.user.TenantID)
		require.NoError(t, w.rig.boot.appDB.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n))
		return n
	}

	b, a := stewardSession(asBSuper), stewardSession(asASuper)
	// A steward assesses its own proposals only, so the one under attack is A's
	// steward's.
	ticket := w.id(w.mustCall(asAOwner, http.StatusCreated, http.MethodPost, "/api/tickets",
		jsonObject{"title": "stewarded " + w.secret, "body": w.secret, "status": "preparing"}))
	proposed, text := callTool(a, "propose_ready", jsonObject{"ticket_id": ticket})
	require.False(t, proposed.IsError, text)
	proposal := proposed.StructuredContent.(map[string]any)["id"].(string)
	for _, tool := range []string{"steward_assess", "steward_draft"} {
		refused, text := call(b, tool, proposal)
		assert.True(t, refused, "%s: tenant B's steward on A's proposal: %s", tool, text)
		assert.NotContains(t, text, w.secret, tool)
		assert.NotContains(t, text, "only the steward", "%s: B's steward was refused as no steward, so no proposal lookup was exercised", tool)
	}
	assert.Zero(t, count("steward_assessments"), "B's steward assessed A's proposal")
	assert.Zero(t, count("steward_drafts"), "B's steward drafted A's proposal")

	for _, tool := range []string{"steward_assess", "steward_draft"} {
		refused, text := call(a, tool, proposal)
		assert.False(t, refused, "%s: tenant A's own steward on A's proposal: %s", tool, text)
	}
	assert.Equal(t, 1, count("steward_assessments"))
	assert.Equal(t, 1, count("steward_drafts"))
}
