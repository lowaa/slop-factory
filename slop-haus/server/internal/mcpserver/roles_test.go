package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (r *realRig) role(name string) uuid.UUID {
	r.t.Helper()
	created, err := r.roles.Create(r.ctx, testPerson(), name, "")
	require.NoError(r.t, err)
	return created.ID
}

// An agent may file a ticket requiring roles, by name -- including roles
// it doesn't hold: adding a restriction widens nothing.
func TestTicketCreate_FilesRequiredRolesByName(t *testing.T) {
	r := newRealRig(t)
	r.role("deploy")
	r.role("review")

	res := r.call("ticket_create", map[string]any{
		"title": "Ship it", "roles": []string{"review", "deploy"}, "role_match": "all",
	})
	require.False(t, res.IsError, resultText(t, res))
	id := structuredID(t, res)
	assert.Contains(t, resultText(t, res),
		fmt.Sprintf("Filed #%d (preparing), claimable only by an agent with all of the roles deploy, review.", id))
	assert.Equal(t, []any{"deploy", "review"}, structured(t, res)["roles"])
	assert.Equal(t, "all", structured(t, res)["role_match"])

	got, err := r.tickets.Get(r.ctx, id)
	require.NoError(t, err)
	assert.Len(t, got.RequiredRoles, 2)
	assert.Equal(t, model.RoleMatchAll, got.RoleMatch)

	read := r.call("ticket_get", map[string]any{"ticket_id": id})
	assert.Contains(t, resultText(t, read), "Claimable only by an agent with all of the roles deploy, review")
	assert.Equal(t, []any{"deploy", "review"}, structured(t, read)["roles"])
}

func TestTicketCreate_AnUnknownRoleNameFilesNothingAndSaysWhatExists(t *testing.T) {
	r := newRealRig(t)
	r.role("deploy")

	res := r.call("ticket_create", map[string]any{"title": "Ship it", "roles": []string{"Deploy"}})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), `"Deploy"`)
	assert.Contains(t, resultText(t, res), "roles are named exactly: deploy")

	listed, err := r.tickets.List(r.ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, listed)
}

// A ticket without roles says nothing about them, in text or structure.
func TestTicketGet_NoRolesLineWithoutRoles(t *testing.T) {
	r := newRealRig(t)
	plain := r.openTicket("Plain")

	read := r.call("ticket_get", map[string]any{"ticket_id": plain.ID})
	assert.NotContains(t, resultText(t, read), "Claimable only")
	assert.NotContains(t, structured(t, read), "roles")
	assert.NotContains(t, structured(t, read), "role_match")
}

// ticket_next for a specific ticket the agent lacks the role for is told
// so -- as an answer, not a tool error to retry -- and is granted nothing.
func TestTicketNext_ADirectedClaimWithoutTheRoleNamesIt(t *testing.T) {
	r := newRealRig(t)
	deploy := r.role("deploy")
	scoped, err := r.tickets.Create(r.ctx, &ticket.CreateParams{
		Title: "Ship it", Status: ticket.StatusOpen, Roles: ticket.RoleRequirement{IDs: []uuid.UUID{deploy}},
	})
	require.NoError(t, err)

	res := r.call("ticket_next", map[string]any{"ticket_id": scoped.ID})
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), fmt.Sprintf("#%d needs role deploy, which you don't have", scoped.ID))
	assert.Contains(t, structured(t, res)["blocked"], "needs role deploy")

	undirected := r.call("ticket_next", nil)
	assert.Contains(t, resultText(t, undirected), "The queue is empty", "and it is not offered undirected either")

	_, err = r.roles.SetAgentRoles(r.ctx, testPerson(), r.agentID, []uuid.UUID{deploy})
	require.NoError(t, err)
	res = r.call("ticket_next", map[string]any{"ticket_id": scoped.ID})
	assert.Contains(t, resultText(t, res), fmt.Sprintf("Granted ticket #%d", scoped.ID), "given the role, it is granted")
}

// ticket_edit has no way to change a ticket's roles: loosening one is a
// person's call. Its schema carries no role field, and one passed anyway
// changes nothing.
func TestTicketEdit_CannotTouchRequiredRoles(t *testing.T) {
	r := newRealRig(t)
	deploy := r.role("deploy")
	_, err := r.roles.SetAgentRoles(r.ctx, testPerson(), r.agentID, []uuid.UUID{deploy})
	require.NoError(t, err)
	held, err := r.tickets.Create(r.ctx, &ticket.CreateParams{
		Title: "Ship it", Status: ticket.StatusOpen, Roles: ticket.RoleRequirement{IDs: []uuid.UUID{deploy}},
	})
	require.NoError(t, err)
	claimed := r.call("ticket_next", map[string]any{"ticket_id": held.ID})
	require.Contains(t, resultText(t, claimed), "Granted")

	tools, err := r.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	schemas := map[string]string{}
	for _, tool := range tools.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)
		schemas[tool.Name] = string(raw)
	}
	require.Contains(t, schemas["ticket_create"], `"roles"`, "the check below can see a role field where there is one")
	require.Contains(t, schemas["ticket_edit"], `"title"`)
	for name, schema := range schemas {
		if name != "ticket_create" {
			assert.NotContains(t, schema, "role", "tool %q takes nothing about roles", name)
		}
	}

	// Refused as an unknown argument or ignored; either way the roles stay.
	r.call("ticket_edit", map[string]any{"revision": 0, "title": "Renamed", "roles": []string{}, "role_match": "any"})
	got, err := r.tickets.Get(r.ctx, held.ID)
	require.NoError(t, err)
	require.Len(t, got.RequiredRoles, 1)
	assert.Equal(t, "deploy", got.RequiredRoles[0].Name)
}

// base_ref reaches the service: with no code host in this rig the service
// refuses it, and the refusal is the tool's text, with nothing filed.
func TestTicketCreate_ForwardsBaseRefAndSurfacesTheServersRefusal(t *testing.T) {
	r := newRealRig(t)

	res := r.call("ticket_create", map[string]any{"title": "From a tag", "base_ref": "v1.2.0"})

	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "invalid base_ref")
	listed, err := r.tickets.List(r.ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, listed)
}
