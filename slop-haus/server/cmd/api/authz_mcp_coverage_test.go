package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// The MCP counterpart of authz_coverage_test.go: every tool the /mcp endpoint
// offers is called as another tenant's agent against tenant A's objects, and
// as the wrong kind of agent for the tools that are a console's or the
// steward's. A tool with no case in testdata/authz/mcp_cases.json (and none
// in its "allow" list) fails TestEveryMCPToolHasAnAuthzCase, naming the tool.
//
// To cover a new tool, add a case for it:
//
//		{"tool": "thing_get", "args": {"ticket_id": "@ticket"}, "foreign": true}
//
//	  - args is the call's arguments; a string "@name" is replaced by the
//	    fixture's id (the REST test's fixtures, plus "proposal", a pending
//	    proposal of A's, and "secret", the text A's objects carry). The args
//	    must pass the tool's schema, or no authorisation is exercised.
//	  - foreign says the call names one of A's objects, so another tenant's
//	    agent must be refused. Leave it out for a tool that acts on the
//	    caller's own tenant or lease: the other tenant's agent then only has
//	    to see none of A's data.
//	  - refusal is for a tool that answers a refusal as a normal result: the
//	    text the answer then holds.
//	  - console_only says the tool is for a console or conductor: a worker is
//	    refused it. Pinned both ways, as the REST test's owner_only is.
//	  - steward_only says the same of the steward, whom a console and a
//	    worker are refused.
const mcpAuthzCasesPath = "testdata/authz/mcp_cases.json"

type mcpAuthzCase struct {
	Tool        string         `json:"tool"`
	Args        map[string]any `json:"args"`
	Foreign     bool           `json:"foreign"`
	ConsoleOnly bool           `json:"console_only"`
	StewardOnly bool           `json:"steward_only"`
	// Refusal is a regexp the refusal's text must match, for a tool that is
	// refused before it reaches the check under test or that answers a
	// refusal as an ordinary result (not an error). Without it a refusal is an
	// error whose text says the object or the caller's kind is not there.
	Refusal string `json:"refusal"`
	Note    string `json:"note"`
}

var defaultRefusal = regexp.MustCompile(`(?i)not found|no such|only a |only the `)

// refused is whether the answer is the refusal the case expects. Any error
// is not enough: a call that fails on its arguments says nothing about
// authorisation.
func (c *mcpAuthzCase) refused(res *toolResult) bool {
	if c.Refusal != "" {
		return regexp.MustCompile(c.Refusal).MatchString(res.text)
	}
	return res.isError && defaultRefusal.MatchString(res.text)
}

type mcpAuthzAllow struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
}

type mcpAuthzTable struct {
	Cases []mcpAuthzCase  `json:"cases"`
	Allow []mcpAuthzAllow `json:"allow"`
}

// The refusals a tool gives for the wrong kind of agent (mcpserver's
// errNotConsole and errNotSteward).
const (
	notConsoleText = "only a console agent"
	notStewardText = "only the steward"
)

func loadMCPAuthzTable(t *testing.T) *mcpAuthzTable {
	t.Helper()
	var table mcpAuthzTable
	raw, err := os.ReadFile(mcpAuthzCasesPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &table))
	return &table
}

var mcpHeaders = map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}

const (
	asAConsole = "a_console"
	asBConsole = "b_console"
	asASteward = "a_steward"
	asBSteward = "b_steward"
)

// mcpWorld is the authz world with an MCP session per agent that can be
// connected: a supervisor is refused at the door, so it has none.
type mcpWorld struct {
	*authzWorld
	agents map[string]*contractAgent
}

func newMCPWorld(t *testing.T) *mcpWorld {
	t.Helper()
	w := &mcpWorld{authzWorld: newAuthzWorld(t), agents: map[string]*contractAgent{}}
	b := w.rig.boot
	for who, spec := range map[string]struct {
		owner string
		kind  string
	}{
		asAConsole: {asAOwner, "console"}, asBConsole: {asBOwner, "console"},
	} {
		d := &oauth.Decision{Approve: true, AgentName: who, Kind: spec.kind}
		w.tokens[who] = b.agentTokenFor(t, w.people[spec.owner], d)
	}
	// Nothing but a supervisor mints the steward, and it hands over its
	// credential.
	for steward, supervisor := range map[string]string{asASteward: asASuper, asBSteward: asBSuper} {
		resp := w.mustCall(supervisor, http.StatusOK, http.MethodPost, "/api/supervisor/steward/credential", nil)
		var tokens struct {
			AccessToken string `json:"access_token"`
		}
		require.NoError(t, json.Unmarshal([]byte(resp.body), &tokens), resp.body)
		w.tokens[steward] = tokens.AccessToken
	}
	for _, who := range []string{asAWorker, asBWorker, asAConsole, asBConsole, asASteward, asBSteward} {
		w.agents[who] = w.connect(who, w.tokens[who])
	}
	// A real proposal of A's, so the tools that take a proposal id are pointed
	// at a row, not at an id that would be refused anyway.
	prep := w.mustCall(asAOwner, http.StatusCreated, http.MethodPost, "/api/tickets",
		jsonObject{"title": "prep " + w.secret, "body": w.secret, "status": "preparing"})
	res := w.agents[asAConsole].mustCall("propose_ready", jsonObject{"ticket_id": w.id(prep)})
	w.fixtures["proposal"] = proposalID(t, res)
	w.leaks = append(w.leaks, w.fixtures["proposal"].(string))
	w.fixtures["secret"] = w.secret
	return w
}

func (w *mcpWorld) connect(name, token string) *contractAgent {
	client := mcp.NewClient(&mcp.Implementation{Name: "authz-" + name, Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: w.base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: token}},
	}, nil)
	require.NoError(w.t, err, name)
	w.t.Cleanup(func() { _ = session.Close() })
	return &contractAgent{rig: w.rig, name: name, token: token, session: session}
}

func (w *mcpWorld) offered() []string {
	res, err := w.agents[asAConsole].session.ListTools(context.Background(), nil)
	require.NoError(w.t, err)
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

// TestEveryMCPToolHasAnAuthzCase is the coverage gate.
func TestEveryMCPToolHasAnAuthzCase(t *testing.T) {
	t.Parallel()
	table := loadMCPAuthzTable(t)
	cases, allowed := map[string]bool{}, map[string]bool{}
	for _, c := range table.Cases {
		assert.False(t, cases[c.Tool], "%s has two cases", c.Tool)
		assert.False(t, c.ConsoleOnly && c.StewardOnly, "%s is both console_only and steward_only", c.Tool)
		cases[c.Tool] = true
	}
	for _, a := range table.Allow {
		assert.NotEmpty(t, a.Reason, "%s is allowlisted without a reason", a.Tool)
		assert.False(t, cases[a.Tool], "%s is both a case and allowlisted", a.Tool)
		allowed[a.Tool] = true
	}
	w := newMCPWorld(t)
	offered := map[string]bool{}
	for _, name := range w.offered() {
		offered[name] = true
		if !cases[name] && !allowed[name] {
			t.Errorf("the MCP tool %q has no authorisation case. Add one to cmd/api/testdata/authz/mcp_cases.json "+
				"(how: the comment at the top of cmd/api/authz_mcp_coverage_test.go), or, if any agent may use it "+
				"on someone else's data on purpose, to its \"allow\" list with the reason", name)
		}
	}
	for name := range cases {
		assert.True(t, offered[name], "mcp_cases.json has a case for %q, which the server does not offer", name)
	}
	for name := range allowed {
		assert.True(t, offered[name], "mcp_cases.json allows %q, which the server does not offer", name)
	}
}

func (w *mcpWorld) args(c *mcpAuthzCase) jsonObject {
	if c.Args == nil {
		return jsonObject{}
	}
	return w.resolve(c.Args).(map[string]any)
}

// call runs the case as who. A tool that is refused or fails is a result with
// isError; a call the tool's schema refuses fails the test, since the case
// would exercise nothing.
func (w *mcpWorld) call(who string, c *mcpAuthzCase) *toolResult {
	w.t.Helper()
	res := w.agents[who].call(c.Tool, w.args(c))
	assert.NotContains(w.t, res.text, "validating", "%s as %s: the case's arguments do not pass the tool's schema, so nothing is exercised", c.Tool, who)
	return res
}

func (w *mcpWorld) noLeak(c *mcpAuthzCase, who string, res *toolResult) {
	w.t.Helper()
	for _, leak := range w.leaks {
		assert.NotContains(w.t, res.rendered, leak, "%s as %s: the answer holds tenant A's data", c.Tool, who)
	}
}

func TestAuthzMCPCoverage(t *testing.T) {
	t.Parallel()
	table := loadMCPAuthzTable(t)
	w := newMCPWorld(t)

	// The door: no token and a supervisor's are turned away before any tool.
	t.Run("not admitted", func(t *testing.T) {
		resp := do(t, &request{method: http.MethodPost, url: w.base + "/mcp", body: mcpInitialize,
			header: mcpHeaders})
		assert.Equal(t, http.StatusUnauthorized, resp.code, resp.body)
		for _, who := range []string{asASuper, asAOwner} {
			resp := do(t, &request{method: http.MethodPost, url: w.base + "/mcp", token: w.tokens[who], body: mcpInitialize,
				header: mcpHeaders})
			assert.Equal(t, http.StatusUnauthorized, resp.code, "%s: %s", who, resp.body)
		}
	})

	// Another tenant's agents, of every kind, against A's objects.
	t.Run("another tenant's agent", func(t *testing.T) {
		for i := range table.Cases {
			c := &table.Cases[i]
			for _, who := range []string{asBWorker, asBConsole, asBSteward} {
				res := w.call(who, c)
				if c.Foreign {
					assert.True(t, c.refused(res), "%s as %s on A's object was not refused: %s", c.Tool, who, res.text)
				}
				w.noLeak(c, who, res)
			}
		}
	})

	// The wrong kind of agent, within tenant A.
	t.Run("the wrong kind of agent", func(t *testing.T) {
		for i := range table.Cases {
			c := &table.Cases[i]
			for _, who := range []string{asAWorker, asAConsole, asASteward} {
				res := w.call(who, c)
				refusedConsole := strings.Contains(res.text, notConsoleText)
				refusedSteward := strings.Contains(res.text, notStewardText)
				switch {
				case c.StewardOnly && who != asASteward:
					assert.True(t, res.isError && refusedSteward, "%s as %s: steward-only, but answered: %s", c.Tool, who, res.text)
				case c.ConsoleOnly && who == asAWorker, c.ConsoleOnly && who == asASteward && c.Tool == "propose_close_merged":
					assert.True(t, res.isError && refusedConsole, "%s as %s: console-only, but answered: %s", c.Tool, who, res.text)
				}
			}
		}
	})

	// Positive control: the rightful caller is not turned away, so a blanket
	// refusal cannot make the tests above pass.
	t.Run("the rightful caller", func(t *testing.T) {
		for i := range table.Cases {
			c := &table.Cases[i]
			who := asAWorker
			switch {
			case c.StewardOnly:
				who = asASteward
			case c.ConsoleOnly:
				who = asAConsole
			}
			res := w.call(who, c)
			assert.NotContains(t, res.text, notConsoleText, "%s as %s", c.Tool, who)
			assert.NotContains(t, res.text, notStewardText, "%s as %s", c.Tool, who)
		}
		assert.False(t, w.call(asAWorker, &mcpAuthzCase{Tool: "checkin"}).isError)
	})
}
