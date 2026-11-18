package agent_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

func TestReplacingAnAlreadyDisabledAgentSucceeds(t *testing.T) {
	f := newOwnerBoundFixture(t)
	_, old := f.heldBy(t, "In flight")
	_, err := f.agents.SetDisabled(f.ctx, old.ID, true)
	require.NoError(t, err)

	newID, err := f.agents.ChooseReplacing(f.ctx, f.owner, old.ID, "successor", "")
	require.NoError(t, err)

	gone, err := f.agents.Get(f.ctx, old.ID)
	require.NoError(t, err)
	assert.NotNil(t, gone.ArchivedAt)
	require.NotNil(t, gone.ReplacedBy)
	assert.Equal(t, newID, gone.ReplacedBy.ID)
}

func TestReplacingDisablesAndArchivesTheOldAgentAndLinksThem(t *testing.T) {
	f := newOwnerBoundFixture(t)
	held, old := f.heldBy(t, "In flight")
	roleSvc := agent.NewRoleService(agent.NewPostgresRoleStore(f.database), slog.DiscardHandler)
	role, err := roleSvc.Create(f.ctx, testPerson(), "builders", "")
	require.NoError(t, err)
	_, err = roleSvc.SetAgentRoles(f.ctx, testPerson(), old.ID, []uuid.UUID{role.ID})
	require.NoError(t, err)

	newID, err := f.agents.ChooseReplacing(f.ctx, f.owner, old.ID, "  successor ", string(agent.KindConsole))
	require.NoError(t, err)

	successor, err := f.agents.Get(f.ctx, newID)
	require.NoError(t, err)
	assert.Equal(t, "successor", successor.Name)
	assert.Equal(t, agent.KindConsole, successor.Kind, "replacing across kinds is allowed")
	assert.Nil(t, successor.DisabledAt)
	require.NotNil(t, successor.Replaces)
	assert.Equal(t, old.ID, successor.Replaces.ID)
	assert.Equal(t, old.Name, successor.Replaces.Name)

	gone, err := f.agents.Get(f.ctx, old.ID)
	require.NoError(t, err)
	assert.NotNil(t, gone.DisabledAt)
	assert.NotNil(t, gone.ArchivedAt)
	assert.Equal(t, old.Name, gone.Name, "history keeps the name")
	require.NotNil(t, gone.ReplacedBy)
	assert.Equal(t, newID, gone.ReplacedBy.ID)
	assert.Equal(t, "successor", gone.ReplacedBy.Name)

	_, err = f.tickets.LeaseFor(f.ctx, old.ID)
	assert.ErrorIs(t, err, ticket.ErrNoLease, "the old agent's lease is ended")
	reopened, err := f.tickets.Get(f.ctx, held.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, reopened.Status, "its ticket returns to the queue")

	roles, err := agent.NewPostgresRoleStore(f.database).RolesFor(f.ctx, newID)
	require.NoError(t, err)
	assert.Empty(t, roles, "the replacement inherits no roles")

	audited := &syncBuffer{}
	logging := agent.NewService(agent.NewPostgresStore(f.database), f.tickets, f.grants, slog.NewJSONHandler(audited, nil),
		agent.WithMembers(tenantRoles{user.NewPostgresStore(f.database)}))
	second, err := logging.Create(f.ctx, "second")
	require.NoError(t, err)
	third, err := logging.ChooseReplacing(f.ctx, f.owner, second.ID, "third", "")
	require.NoError(t, err)
	events := audited.events(t, "agent_replaced")
	require.Len(t, events, 1)
	assert.Equal(t, third.String(), events[0]["agent"])
	assert.Equal(t, second.ID.String(), events[0]["replaced_agent"])
}

func TestAFailedReplacementLeavesTheOldAgentAlone(t *testing.T) {
	f := newOwnerBoundFixture(t)
	old, err := f.agents.Create(f.ctx, "old")
	require.NoError(t, err)
	_, err = f.agents.Create(f.ctx, "taken")
	require.NoError(t, err)

	_, err = f.agents.ChooseReplacing(f.ctx, f.owner, old.ID, "taken", "")

	assert.ErrorIs(t, err, oauth.ErrAgentNameTaken)
	after, err := f.agents.Get(f.ctx, old.ID)
	require.NoError(t, err)
	assert.Nil(t, after.DisabledAt, "the transaction rolled back")
	assert.Nil(t, after.ArchivedAt)
	assert.Nil(t, after.ReplacedBy)
}

// The duplicate name above fails before the old agent is touched, so it
// cannot show the transaction undoing work: this fails the last step.
func TestAReplacementThatFailsAfterDisablingTheOldAgentIsUndone(t *testing.T) {
	app, owner := dbtest.WithOwnerDB(t)
	ctx := db.WithTenant(context.Background(), dbtest.NewTenant(t, app))
	tickets := ticket.NewService(ticket.NewPostgresStore(app), slog.DiscardHandler)
	agents := agent.NewService(agent.NewPostgresStore(app), tickets, &recordingGrants{}, slog.DiscardHandler)
	old, err := agents.Create(ctx, "old")
	require.NoError(t, err)
	_, err = owner.Global().Exec(context.Background(), `
		CREATE FUNCTION refuse_archive() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.archived_at IS NOT NULL THEN RAISE EXCEPTION 'archiving refused'; END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER refuse_archive BEFORE UPDATE ON agents FOR EACH ROW EXECUTE FUNCTION refuse_archive()`)
	require.NoError(t, err)

	_, err = agents.ChooseReplacing(ctx, &authn.User{ID: uuid.New()}, old.ID, "successor", "")

	assert.ErrorContains(t, err, "archiving refused")
	after, err := agents.Get(ctx, old.ID)
	require.NoError(t, err)
	assert.Nil(t, after.DisabledAt, "the disable was rolled back")
	listed, err := agents.List(ctx, true)
	require.NoError(t, err)
	assert.Len(t, listed, 1, "no successor was left behind")
}

func TestTwoReplacementsOfOneAgentLetExactlyOneWin(t *testing.T) {
	f := newOwnerBoundFixture(t)
	old, err := f.agents.Create(f.ctx, "old")
	require.NoError(t, err)

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		go func() {
			<-start
			_, err := f.agents.ChooseReplacing(f.ctx, f.owner, old.ID, name, "")
			errs <- err
		}()
	}
	close(start)
	results := []error{<-errs, <-errs}

	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
			continue
		}
		assert.ErrorIs(t, err, oauth.ErrReplaceUnavailable, "the loser finds it already replaced")
	}
	assert.Equal(t, 1, succeeded)
	listed, err := f.agents.List(f.ctx, true)
	require.NoError(t, err)
	assert.Len(t, listed, 2, "the old agent and one successor")
}

func TestAnAgentCanBeReplacedOnlyOnce(t *testing.T) {
	f := newOwnerBoundFixture(t)
	old, err := f.agents.Create(f.ctx, "old")
	require.NoError(t, err)
	_, err = f.agents.ChooseReplacing(f.ctx, f.owner, old.ID, "first", "")
	require.NoError(t, err)

	_, err = f.agents.ChooseReplacing(f.ctx, f.owner, old.ID, "second", "")
	assert.ErrorIs(t, err, oauth.ErrReplaceUnavailable, "archived")

	// Unarchived, it is no longer put away, but the database still allows
	// one replacement per agent.
	_, err = f.agents.SetArchived(f.ctx, f.admin, old.ID, false)
	require.NoError(t, err)
	_, err = f.agents.ChooseReplacing(f.ctx, f.owner, old.ID, "third", "")
	assert.ErrorIs(t, err, oauth.ErrReplaceUnavailable, "already replaced")
	_, err = f.database.Exec(f.ctx, `INSERT INTO agents (id, name, kind, replaces_agent_id) VALUES (gen_random_uuid(), 'rogue', 'worker', $1)`, old.ID)
	assert.ErrorContains(t, err, "agents_one_replacement")
}

func TestOnlyAConductorsOwnerMayReplaceIt(t *testing.T) {
	f := newOwnerBoundFixture(t)
	c := f.register(t, f.owner, "mine")

	for name, who := range map[string]*authn.User{"another member": f.other, "the tenant's owner": f.admin} {
		_, err := f.agents.ChooseReplacing(f.ctx, who, c.ID, "taken-over", "")
		assert.ErrorIs(t, err, oauth.ErrReplaceUnavailable, name)
	}
	after, err := f.agents.Get(f.ctx, c.ID)
	require.NoError(t, err)
	assert.Nil(t, after.DisabledAt)

	newID, err := f.agents.ChooseReplacing(f.ctx, f.owner, c.ID, "console-now", string(agent.KindConsole))
	require.NoError(t, err)
	gone, err := f.agents.Get(f.ctx, c.ID)
	require.NoError(t, err)
	assert.NotNil(t, gone.ArchivedAt)
	assert.Equal(t, newID, gone.ReplacedBy.ID)
}

func TestReplacementLinksOverREST(t *testing.T) {
	f := newConductorFixture(t)
	old, err := f.agents.Create(f.ctx, "old")
	require.NoError(t, err)
	c := f.register(t, f.owner, "machine")
	newID, err := f.agents.ChooseReplacing(f.ctx, f.owner, old.ID, "new", "")
	require.NoError(t, err)
	_, err = f.agents.ChooseReplacing(f.ctx, f.owner, c.ID, "newer", "")
	require.NoError(t, err)
	api := f.api(f.tokens())

	rec := api.do("owner", http.MethodGet, "/api/agents?include_archived=true", ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct{ Agents []agent.AgentBody }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	byName := map[string]agent.AgentBody{}
	for _, a := range out.Agents {
		byName[a.Name] = a
	}
	require.NotNil(t, byName["old"].ReplacedBy)
	assert.Equal(t, agent.AgentRefBody{ID: newID.String(), Name: "new"}, *byName["old"].ReplacedBy)
	require.NotNil(t, byName["new"].Replaces)
	assert.Equal(t, "old", byName["new"].Replaces.Name)

	rec = api.do("owner", http.MethodGet, "/api/conductors?include_archived=true", ``)
	require.Equal(t, http.StatusOK, rec.Code)
	var conductors struct{ Conductors []agent.ConductorBody }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &conductors))
	require.Len(t, conductors.Conductors, 1)
	require.NotNil(t, conductors.Conductors[0].ReplacedBy)
	assert.Equal(t, "newer", conductors.Conductors[0].ReplacedBy.Name)
}
