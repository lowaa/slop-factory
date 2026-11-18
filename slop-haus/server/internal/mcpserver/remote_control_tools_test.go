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
)

// The Remote Control host starts pi with an allowlist of tool names (the
// manifest's console role), because pi drops a tool the allowlist does not
// name before the extension registers it. A console session lists tools the
// server hands it; every one of them but the supervisor's has to be in that
// role, and the role may name nothing else.
func TestRemoteControlHost_ConsoleRoleIsWhatAConsoleIsOffered(t *testing.T) {
	// A deployed server wires every optional family of tools (the change
	// tools, and the proposal tools a console is listed), so this one has to,
	// or a tool missing from the role would go unseen here.
	database := dbtest.WithDB(t)
	rig := newRealRigWith(t, database, &realRigOptions{changes: &fakeChanges{}, manifests: manifest.NewPostgresStore(database), batches: landing.NewBatchStore(database), attachments: newAttachmentService(t, database, nil), reviews: true})
	res, err := rig.agentSession("console", true).session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	offered := map[string]bool{}
	for _, tool := range res.Tools {
		offered[tool.Name] = true
	}
	for _, family := range []string{"change_open", "propose_ready", "workspace_get", "review_request"} {
		require.True(t, offered[family], "the rig no longer wires the tools %q belongs to", family)
	}

	roles := contracts.LoadTools(t).Roles
	listed := map[string]bool{}
	for _, name := range roles.Console {
		listed[name] = true
		assert.True(t, offered[name], "the console role names %q, which a console is not offered", name)
	}
	skip := map[string]bool{}
	for _, name := range roles.DeniedToHeadless {
		skip[name] = true
	}
	for name := range offered {
		if skip[name] {
			continue
		}
		assert.True(t, listed[name], "a console is offered %q, which the console role leaves out", name)
	}
}
