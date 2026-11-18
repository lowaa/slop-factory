package mcpserver_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/contracts"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

// The contracts manifest names tools by role and each client holds its own
// lists to it. A tool the server offers has to have a role, and a role may not
// name a tool the server does not offer, so a retired or renamed tool cannot
// linger in a deny list (ticket_wait did) or vanish from an allowlist (pi's
// ticket_next).
func TestToolRoles_CoverExactlyWhatTheServerOffers(t *testing.T) {
	offered := offeredTools(t)
	roles := contracts.LoadTools(t).Roles

	inRole := map[string]bool{}
	for name, list := range map[string][]string{
		"console":            roles.Console,
		"steward":            roles.Steward,
		"denied_to_headless": roles.DeniedToHeadless,
		"denied_to_steward":  roles.DeniedToSteward,
	} {
		for _, tool := range list {
			assert.True(t, offered[tool], "role %s names %q, which the server does not offer", name, tool)
			inRole[tool] = true
		}
	}
	for tool := range offered {
		assert.True(t, inRole[tool], "the server offers %q, which no role in contracts/tools.json names", tool)
	}
}

// A Remote Control session is offered the whole server, less what belongs to
// the supervisor (claiming) and to the steward alone.
func TestConsoleRole_IsTheServerLessTheSupervisorsAndStewards(t *testing.T) {
	roles := contracts.LoadTools(t).Roles
	console := map[string]bool{}
	for _, tool := range roles.Console {
		console[tool] = true
	}

	for _, tool := range roles.DeniedToHeadless {
		assert.False(t, console[tool], "the console role must not be offered %q", tool)
	}
	for tool := range offeredTools(t) {
		if tool == "steward_assess" || tool == "steward_draft" || tool == "steward_label_discussion" {
			continue
		}
		var denied bool
		for _, d := range roles.DeniedToHeadless {
			denied = denied || d == tool
		}
		assert.True(t, console[tool] || denied, "the console role leaves out %q", tool)
	}
}

// offeredTools is every tool a deployed server can offer anyone: all the
// optional families wired, listed as a console and as a steward.
func offeredTools(t *testing.T) map[string]bool {
	t.Helper()
	database := dbtest.WithDB(t)
	rig := newRealRigWith(t, database, &realRigOptions{
		changes:     &fakeChanges{},
		manifests:   manifest.NewPostgresStore(database),
		batches:     landing.NewBatchStore(database),
		attachments: newAttachmentService(t, database, nil),
		steward:     &steward.Config{Level: steward.LevelShadow, Mode: steward.ModeShadow, DailyCap: 1},
		reviews:     true,
	})
	offered := map[string]bool{}
	for _, session := range []*consoleSession{rig.agentSession("console", true), rig.stewardSession("steward")} {
		res, err := session.session.ListTools(context.Background(), nil)
		require.NoError(t, err)
		for _, tool := range res.Tools {
			offered[tool.Name] = true
		}
	}
	for _, family := range []string{"change_open", "propose_ready", "steward_assess", "steward_draft", "steward_label_discussion", "workspace_get", "ticket_finish", "review_request"} {
		require.True(t, offered[family], "the rig no longer wires the tools %q belongs to", family)
	}
	return offered
}
