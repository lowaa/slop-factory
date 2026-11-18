package apiserver_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// DefaultDeny refuses an unmarked route at runtime; this finds one before
// it ships, by name.
func TestEveryOperationChoosesExactlyOneAuthMarker(t *testing.T) {
	spec := apiserver.BuildOpenAPI()
	require.NotEmpty(t, spec.Paths)

	for path, item := range spec.Paths {
		for method, op := range map[string]*huma.Operation{
			"GET": item.Get, "POST": item.Post, "PUT": item.Put, "PATCH": item.Patch, "DELETE": item.Delete,
		} {
			if op == nil {
				continue
			}
			assert.Equal(t, 1, authn.Markers(op), "%s %s (%s)", method, path, op.OperationID)
		}
	}
}

// securityCriticalMarkers is what each of these operations must carry, by
// OperationID: wrong is as dangerous as missing, so this checks the exact
// marker rather than just that one is present (that's
// TestEveryOperationChoosesExactlyOneAuthMarker's job). Built against
// BuildOpenAPI's real operations, so a route renamed or dropped here fails
// the test rather than silently going unchecked.
var securityCriticalMarkers = map[string]string{
	// Every /api/admin/* route, plus the admin-check nginx uses to gate the
	// admin pages.
	"createAdminInvite": "admin",
	"listAdminInvites":  "admin",
	"revokeAdminInvite": "admin",
	"adminCheck":        "admin",

	// Open to anyone: signing up, signing in, and the emailed-link
	// exchanges that come before a session exists.
	"register":                 "public",
	"login":                    "public",
	"activate":                 "public",
	"requestPasswordReset":     "public",
	"resetPassword":            "public",
	"refreshSession":           "public",
	"logout":                   "public",
	"previewInvite":            "public",
	"acceptInviteAsNewAccount": "public",
	"getAuthConfig":            "public",

	// A signed-in user's tenant: agents, invites, tenant membership, OAuth
	// consent and device approval, settings and tickets.
	"listAgents":               "user",
	"createAgent":              "user",
	"createAgentSetupScript":   "user",
	"getSupervisorSetupScript": "user",
	"getSupervisorPackage":     "agent",
	"rotateAgentSetupScript":   "user",
	"getAgent":                 "user",
	"setAgentDisabled":         "user",
	// A conductor belongs to the person who registered it: an agent able to
	// reach any of these could register one, or switch one off, for itself.
	"registerConductor":          "user",
	"listConductors":             "user",
	"setConductorDisabled":       "user",
	"rotateConductorSetupScript": "user",
	// Conversations are the owner's alone, and the host's routes are the
	// conductor's own credential's: a worker's token, or a person's, must not
	// reach the other's.
	"startConversation":       "user",
	"listConversations":       "user",
	"getConversation":         "user",
	"listConversationEvents":  "user",
	"sendConversationMessage": "user",
	"disconnectConversation":  "user",
	"streamConversation":      "user",
	"pollConductorWork":       "agent",
	"reportConductorEvents":   "agent",
	"reportConductorLayout":   "agent",
	"createInvite":            "user",
	"listInvites":             "user",
	"revokeInvite":            "user",
	"acceptInvite":            "user",
	"listTenants":             "user",
	"switchTenant":            "user",
	"getAuthRequest":          "user",
	"decideAuthRequest":       "user",
	"lookupDeviceCode":        "user",
	"decideDeviceCode":        "user",
	"listGrants":              "user",
	"revokeGrant":             "user",
	"listSettings":            "user",
	"getSetting":              "user",
	"updateSetting":           "user",
	"listTickets":             "user",
	"createTicket":            "user",
	"getTicket":               "user",
	"updateTicket":            "user",
	"listTicketComments":      "user",
	"createTicketComment":     "user",
	"listTicketAgentContexts": "user",
	"getBlockingGraph":        "user",
	"getTicketBlockers":       "user",
	"getBlockerCandidates":    "user",
	"addTicketBlocker":        "user",
	"removeTicketBlocker":     "user",
	// Changing a ticket's required roles can loosen a restriction on who
	// may claim it: a person's call only.
	"setTicketRoles": "user",

	// A console's proposals are decided by a person only: an agent able to
	// reach any of these could approve its own.
	"listProposals":       "user",
	"getProposal":         "user",
	"approveProposal":     "user",
	"rejectProposal":      "user",
	"approveAllProposals": "user",
	"rejectAllProposals":  "user",

	// Roles gate which tickets an agent may claim: an agent must never
	// reach these, or it could grant itself work.
	"listAgentRoles":  "user",
	"createAgentRole": "user",
	"updateAgentRole": "user",
	"deleteAgentRole": "user",
	"setAgentRoles":   "user",

	// Harness config chooses what runs in every agent's process: a
	// person's to read and set. An agent reads only its own, below.
	"getHarnessConfig":          "user",
	"saveHarnessConfigDefaults": "user",
	"saveAgentHarnessConfig":    "user",

	// Fleet instructions land in every agent's prompt: an agent able to write
	// them could steer the rest of the fleet. Agents only receive the default,
	// inside the scripts their workspace routes render.
	"getFleetInstructions":         "user",
	"saveFleetInstructionsVersion": "user",
	"setFleetInstructionsDefault":  "user",

	"archiveFleetInstructionsVersion":   "user",
	"unarchiveFleetInstructionsVersion": "user",
	"archiveFleetInstructionsGroup":     "user",
	"unarchiveFleetInstructionsGroup":   "user",

	// The agent's own bearer token: hook callbacks and its workspace update.
	"stopHook":                    "agent",
	"sessionStartHook":            "agent",
	"getWorkspaceUpdateScript":    "agent",
	"getWorkspaceManifestForSlot": "agent",
	"getWorkspaceHarnessConfig":   "agent",
}

// TestSecurityCriticalOperationsCarryTheRightMarker guards against an
// operation that has exactly one marker (satisfying the test above) but the
// wrong one -- a copy-pasted RequireUser on what should be RequireAdmin
// would pass that test and still be a privilege escalation.
func TestSecurityCriticalOperationsCarryTheRightMarker(t *testing.T) {
	spec := apiserver.BuildOpenAPI()
	require.NotEmpty(t, spec.Paths)

	found := make(map[string]bool, len(securityCriticalMarkers))
	for path, item := range spec.Paths {
		for method, op := range map[string]*huma.Operation{
			"GET": item.Get, "POST": item.Post, "PUT": item.Put, "PATCH": item.Patch, "DELETE": item.Delete,
		} {
			if op == nil {
				continue
			}
			want, ok := securityCriticalMarkers[op.OperationID]
			if !ok {
				continue
			}
			found[op.OperationID] = true
			assert.Equal(t, want, authn.MarkerName(op), "%s %s (%s)", method, path, op.OperationID)
		}
	}
	for id := range securityCriticalMarkers {
		assert.True(t, found[id], "operation %q was not registered", id)
	}
}

// The frontend uses the generated types as they are, so the spec must say
// where the server sends null: wireschema's pass has to have run over the
// real registration, not only over its own tests' types.
func TestSpecMarksNullWhereTheServerSendsIt(t *testing.T) {
	schemas := apiserver.BuildOpenAPI().Components.Schemas.Map()

	agent := schemas["AgentBody"]
	require.NotNil(t, agent)
	require.Len(t, agent.Properties["lease"].AnyOf, 2, "a nil lease is sent as null")
	assert.False(t, agent.Properties["roles"].Nullable, "roles is never null")

	ticketBody := schemas["TicketBody"]
	require.NotNil(t, ticketBody)
	assert.Equal(t, "#/components/schemas/Status", ticketBody.Properties["status"].Ref)
	assert.False(t, ticketBody.Properties["paths"].Nullable)

	assert.True(t, schemas["RevisionBody"].Properties["body_diff"].Nullable, "null when the body was left alone")
}

type nilSlicesBody struct {
	Items []string `json:"items"`
}

// What lets the spec say a response's slices are never null: the real
// config sends a nil one as empty.
func TestConfigSendsNilSlicesAsEmpty(t *testing.T) {
	_, api := humatest.New(t, apiserver.NewConfig("test", "1.0.0"))
	huma.Get(api, "/items", func(context.Context, *struct{}) (*struct{ Body nilSlicesBody }, error) {
		return &struct{ Body nilSlicesBody }{}, nil
	})

	resp := api.Get("/items")
	require.Equal(t, http.StatusOK, resp.Code)
	assert.JSONEq(t, `{"items": []}`, resp.Body.String())
}

// A setup script is served as a shell script, not JSON; a client generated
// from the spec must not try to parse it as JSON.
func TestSetupScriptsAreDeclaredAsShellScripts(t *testing.T) {
	spec := apiserver.BuildOpenAPI()
	for path, want := range map[string]struct {
		op     func(*huma.PathItem) *huma.Operation
		status string
	}{
		"/api/agents/setup_script":          {func(p *huma.PathItem) *huma.Operation { return p.Post }, "201"},
		"/api/agents/{id}/setup_script":     {func(p *huma.PathItem) *huma.Operation { return p.Post }, "200"},
		"/api/workspace/update_script":      {func(p *huma.PathItem) *huma.Operation { return p.Get }, "200"},
		"/api/conductors":                   {func(p *huma.PathItem) *huma.Operation { return p.Post }, "201"},
		"/api/conductors/{id}/setup_script": {func(p *huma.PathItem) *huma.Operation { return p.Post }, "200"},
	} {
		require.Contains(t, spec.Paths, path)
		op := want.op(spec.Paths[path])
		require.NotNil(t, op, path)
		require.Contains(t, op.Responses, want.status, path)
		content := op.Responses[want.status].Content
		assert.Len(t, content, 1, path)
		require.Contains(t, content, "text/x-shellscript", path)
		assert.Equal(t, huma.TypeString, content["text/x-shellscript"].Schema.Type, path)
	}
}

// Each Go enum's schema carries exactly its values: the frontend's unions
// are these, and huma refuses a request value outside them.
func TestEnumSchemasCarryTheirValues(t *testing.T) {
	schemas := apiserver.BuildOpenAPI().Components.Schemas.Map()

	statuses := make([]any, 0)
	for _, s := range model.Statuses() {
		statuses = append(statuses, string(s))
	}
	for name, want := range map[string][]any{
		"Status":     statuses,
		"Priority":   {"low", "normal", "high", "urgent"},
		"PathSource": {"filed", "extended"},
	} {
		require.Contains(t, schemas, name)
		assert.Equal(t, want, schemas[name].Enum, name)
	}
	assert.Equal(t, []any{"string", "int", "float", "bool"}, schemas["SettingBody"].Properties["type"].Enum)
}

// A supervisor creates workers and mints their credentials, so the agent
// routes it may reach are the ones this names and no others. Every other
// agent route is closed to it by default; adding one here is deciding that a
// supervisor's credential may do that.
var supervisorReachable = map[string]string{
	"createSupervisorSlot":                    "only",
	"retireSupervisorSlot":                    "only",
	"adoptSupervisorSlot":                     "only",
	"listSupervisorSlots":                     "only",
	"issueSupervisorSlotCredential":           "only",
	"getOwnDesiredState":                      "only",
	"reportSupervisorObserved":                "only",
	"ensureSupervisorSteward":                 "only",
	"issueSupervisorStewardCredential":        "only",
	"revokeSupervisorStewardCredential":       "only",
	"ensureSupervisorRemoteControlHost":       "only",
	"issueSupervisorRemoteControlCredential":  "only",
	"revokeSupervisorRemoteControlCredential": "only",
	"getSupervisorStewardBriefing":            "only",
	"getSupervisorView":                       "only",
	"listSupervisorStewardEvents":             "only",
	"acquireSupervisorStewardLease":           "only",
	"releaseSupervisorStewardLease":           "only",
	"getSupervisorStewardAgreement":           "only",
	"getSupervisorStewardMode":                "only",
	"pollWorker":                              "also",
	"getWorkspaceUpdateScript":                "also",
	"getWorkspaceManifestForSlot":             "also",
	// The supervisor runs slop-review for its slots and posts their runs.
	"ingestReviewRun":         "also",
	"getReviewProviderConfig": "also",
}

func TestOnlyTheNamedAgentRoutesServeASupervisor(t *testing.T) {
	spec := apiserver.BuildOpenAPI()
	reachable := map[string]string{}
	for _, item := range spec.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil || authn.MarkerName(op) != "agent" {
				continue
			}
			if authn.SupervisorAllowed(op, true) {
				mode := "also"
				if !authn.SupervisorAllowed(op, false) {
					mode = "only"
				}
				reachable[op.OperationID] = mode
			}
		}
	}
	assert.Equal(t, supervisorReachable, reachable)
}
