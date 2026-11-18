package agent_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
)

func (f *conductorFixture) disabledWorker(t *testing.T, name string) *agent.Agent {
	t.Helper()
	w, err := f.agents.Create(f.ctx, name)
	require.NoError(t, err)
	_, err = f.agents.SetDisabled(f.ctx, w.ID, true)
	require.NoError(t, err)
	return w
}

func (f *conductorFixture) names(t *testing.T, includeArchived bool) []string {
	t.Helper()
	listed, err := f.agents.List(f.ctx, includeArchived)
	require.NoError(t, err)
	out := []string{}
	for _, a := range listed {
		out = append(out, a.Name)
	}
	return out
}

// The rule is the database's, so a writer that skips the service cannot
// break it either.
func TestTheDatabaseRefusesAnArchivedEnabledAgent(t *testing.T) {
	f := newConductorFixture(t)
	live, err := f.agents.Create(f.ctx, "live")
	require.NoError(t, err)
	off := f.disabledWorker(t, "off")

	_, err = f.database.Exec(f.ctx, `UPDATE agents SET archived_at = now() WHERE id = $1`, live.ID)
	assert.ErrorContains(t, err, "agents_archived_is_disabled", "archiving an enabled agent")

	_, err = f.database.Exec(f.ctx, `UPDATE agents SET archived_at = now() WHERE id = $1`, off.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE agents SET disabled_at = NULL WHERE id = $1`, off.ID)
	assert.ErrorContains(t, err, "agents_archived_is_disabled", "enabling an archived agent")
}

func TestArchivingNeedsADisabledAgentAndKeepsItDisabled(t *testing.T) {
	f := newConductorFixture(t)
	live, err := f.agents.Create(f.ctx, "live")
	require.NoError(t, err)
	_, err = f.agents.SetArchived(f.ctx, f.admin, live.ID, true)
	assert.ErrorIs(t, err, agent.ErrNotDisabled)

	off := f.disabledWorker(t, "off")
	first, err := f.agents.SetArchived(f.ctx, f.admin, off.ID, true)
	require.NoError(t, err)
	require.NotNil(t, first.ArchivedAt)
	again, err := f.agents.SetArchived(f.ctx, f.admin, off.ID, true)
	require.NoError(t, err)
	assert.True(t, first.ArchivedAt.Equal(*again.ArchivedAt), "archiving twice keeps the first time")

	back, err := f.agents.SetArchived(f.ctx, f.admin, off.ID, false)
	require.NoError(t, err)
	assert.Nil(t, back.ArchivedAt)
	assert.NotNil(t, back.DisabledAt, "unarchiving does not enable it")
}

func TestAnArchivedAgentCannotBeEnabledUntilUnarchived(t *testing.T) {
	f := newConductorFixture(t)
	off := f.disabledWorker(t, "off")
	_, err := f.agents.SetArchived(f.ctx, f.admin, off.ID, true)
	require.NoError(t, err)

	_, err = f.agents.SetDisabled(f.ctx, off.ID, false)
	assert.ErrorIs(t, err, agent.ErrArchived)
	got, err := f.agents.Get(f.ctx, off.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.DisabledAt)
	assert.NotNil(t, got.ArchivedAt)

	_, err = f.agents.SetArchived(f.ctx, f.admin, off.ID, false)
	require.NoError(t, err)
	enabled, err := f.agents.SetDisabled(f.ctx, off.ID, false)
	require.NoError(t, err)
	assert.Nil(t, enabled.DisabledAt)
}

func TestEnablingAnAgentThatDoesNotExistIsNotFoundNotArchived(t *testing.T) {
	f := newConductorFixture(t)
	_, err := f.agents.SetDisabled(f.ctx, uuid.New(), false)
	assert.ErrorIs(t, err, agent.ErrNotFound)
}

func TestTheListLeavesOutArchivedAgentsUnlessAskedButTheyStillResolve(t *testing.T) {
	f := newConductorFixture(t)
	_, err := f.agents.Create(f.ctx, "live")
	require.NoError(t, err)
	off := f.disabledWorker(t, "off")
	_ = f.disabledWorker(t, "off-but-listed")
	_, err = f.agents.SetArchived(f.ctx, f.admin, off.ID, true)
	require.NoError(t, err)

	assert.Equal(t, []string{"live", "off-but-listed"}, f.names(t, false))
	assert.Equal(t, []string{"live", "off", "off-but-listed"}, f.names(t, true))

	byID, err := f.agents.Get(f.ctx, off.ID)
	require.NoError(t, err)
	assert.Equal(t, "off", byID.Name)
	name, err := f.agents.Name(f.ctx, off.ID)
	require.NoError(t, err)
	assert.Equal(t, "off", name, "history still names an archived agent")
}

func TestTheConductorListsLeaveOutArchivedConductorsUnlessAsked(t *testing.T) {
	f := newConductorFixture(t)
	kept := f.register(t, f.owner, "kept")
	gone := f.register(t, f.owner, "gone")
	_, err := f.conductors.SetDisabled(f.ctx, f.owner, gone.ID, true)
	require.NoError(t, err)
	_, err = f.conductors.SetArchived(f.ctx, f.owner, gone.ID, true)
	require.NoError(t, err)

	own, err := f.conductors.ListOwned(f.ctx, f.owner, false)
	require.NoError(t, err)
	require.Len(t, own, 1)
	assert.Equal(t, kept.ID, own[0].ID)
	own, err = f.conductors.ListOwned(f.ctx, f.owner, true)
	require.NoError(t, err)
	assert.Len(t, own, 2)

	others, err := f.conductors.ListOthers(f.ctx, f.admin, false)
	require.NoError(t, err)
	assert.Len(t, others, 1)
	others, err = f.conductors.ListOthers(f.ctx, f.admin, true)
	require.NoError(t, err)
	assert.Len(t, others, 2)
}

func TestWhoMayArchiveAConductor(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	_, err := f.conductors.SetArchived(f.ctx, f.owner, c.ID, true)
	assert.ErrorIs(t, err, agent.ErrNotDisabled, "still enabled")
	_, err = f.conductors.SetDisabled(f.ctx, f.owner, c.ID, true)
	require.NoError(t, err)

	_, err = f.conductors.SetArchived(f.ctx, f.other, c.ID, true)
	assert.ErrorIs(t, err, agent.ErrNotOwner, "another member")

	archived, err := f.conductors.SetArchived(f.ctx, f.admin, c.ID, true)
	require.NoError(t, err)
	assert.NotNil(t, archived.ArchivedAt, "the tenant's owner may")

	_, err = f.conductors.SetArchived(f.ctx, f.other, c.ID, false)
	assert.ErrorIs(t, err, agent.ErrNotOwner)
	_, err = f.conductors.SetArchived(f.ctx, f.owner, c.ID, false)
	require.NoError(t, err)

	w := f.disabledWorker(t, "worker")
	_, err = f.conductors.SetArchived(f.ctx, f.admin, w.ID, true)
	assert.ErrorIs(t, err, agent.ErrNotFound, "a worker is not a conductor")
}

func TestArchivingAWorkerOverREST(t *testing.T) {
	f := newConductorFixture(t)
	api := f.api(f.tokens())
	w := f.disabledWorker(t, "retired")
	live, err := f.agents.Create(f.ctx, "live")
	require.NoError(t, err)
	path := "/api/agents/" + w.ID.String()

	rec := api.do("an-agents-token", http.MethodPost, path+"/archive", ``)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "an agent's token cannot archive")

	rec = api.do("owner", http.MethodPost, "/api/agents/"+live.ID.String()+"/archive", ``)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	rec = api.do("owner", http.MethodPost, path+"/archive", ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body agent.AgentBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotNil(t, body.ArchivedAt)

	rec = api.do("owner", http.MethodPatch, path, `{"disabled":false}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "enabling an archived agent")
	assert.Contains(t, rec.Body.String(), "unarchive")

	listed := func(query string) []string {
		rec := api.do("owner", http.MethodGet, "/api/agents"+query, ``)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out struct{ Agents []agent.AgentBody }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		names := []string{}
		for _, a := range out.Agents {
			names = append(names, a.Name)
		}
		return names
	}
	assert.Equal(t, []string{"live"}, listed(""))
	assert.Equal(t, []string{"live", "retired"}, listed("?include_archived=true"))

	rec = api.do("owner", http.MethodPost, path+"/unarchive", ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"live", "retired"}, listed(""))
}

func TestArchivingAConductorOverREST(t *testing.T) {
	f := newConductorFixture(t)
	api := f.api(f.tokens())
	c := f.register(t, f.owner, "mine")
	path := "/api/conductors/" + c.ID.String()

	rec := api.do("owner", http.MethodPost, path+"/archive", ``)
	assert.Equal(t, http.StatusConflict, rec.Code, "enabled")
	require.Equal(t, http.StatusOK, api.do("owner", http.MethodPatch, path, `{"disabled":true}`).Code)
	rec = api.do("other", http.MethodPost, path+"/archive", ``)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = api.do("admin", http.MethodPost, path+"/archive", ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body agent.ConductorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotNil(t, body.ArchivedAt)

	rec = api.do("owner", http.MethodPatch, path, `{"disabled":false}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "enabling an archived conductor")

	count := func(query string) int {
		rec := api.do("owner", http.MethodGet, "/api/conductors"+query, ``)
		require.Equal(t, http.StatusOK, rec.Code)
		var out struct{ Conductors []agent.ConductorBody }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return len(out.Conductors)
	}
	assert.Equal(t, 0, count(""))
	assert.Equal(t, 1, count("?include_archived=true"))

	require.Equal(t, http.StatusOK, api.do("owner", http.MethodPost, path+"/unarchive", ``).Code)
	assert.Equal(t, 1, count(""))
}
