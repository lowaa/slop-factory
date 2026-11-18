package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// audit is the "audit" log lines with the given action, as field maps.
func (b *lockedBuffer) audit(t *testing.T, action string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		if rec["msg"] == "audit" && rec["action"] == action {
			out = append(out, rec)
		}
	}
	return out
}

type supervisorFixture struct {
	*consoleFixture
	logs *lockedBuffer
}

func newSupervisorFixture(t *testing.T) *supervisorFixture {
	t.Helper()
	return withLogs(t, newConsoleFixture(t))
}

func withLogs(t *testing.T, base *consoleFixture) *supervisorFixture {
	t.Helper()
	f := &supervisorFixture{consoleFixture: base, logs: &lockedBuffer{}}
	f.agents = agent.NewService(agent.NewPostgresStore(f.database), f.tickets, nil, slog.NewJSONHandler(f.logs, nil),
		agent.WithProposalVoider(f.pending))
	return f
}

// supervisorIn registers a supervisor in the tenant of ctx. Nothing creates
// one through the service yet, so the row is written directly.
func (f *supervisorFixture) supervisorIn(t *testing.T, ctx context.Context, name string, slotCap int) *agent.Agent {
	t.Helper()
	id := uuid.New()
	_, err := f.database.Exec(ctx, `INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, $2, 'supervisor', $3)`, id, name, slotCap)
	require.NoError(t, err)
	got, err := f.agents.Get(ctx, id)
	require.NoError(t, err)
	return got
}

func (f *supervisorFixture) supervisor(t *testing.T, name string, slotCap int) *agent.Agent {
	t.Helper()
	return f.supervisorIn(t, f.ctx, name, slotCap)
}

func (f *supervisorFixture) slot(t *testing.T, sup *agent.Agent, name string) *agent.Agent {
	t.Helper()
	slot, err := f.agents.CreateSlot(f.ctx, sup, name)
	require.NoError(t, err)
	return slot
}

func (f *supervisorFixture) grant(t *testing.T, a *agent.Agent) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.database.Global().Exec(context.Background(), `
		INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, resource, scope)
		VALUES ($1, $2, $3, 'slop-haus-workspace', 'r', 'agent')`, id, f.tenant, a.ID)
	require.NoError(t, err)
	return id
}

func (f *supervisorFixture) grantRevoked(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	var revoked bool
	require.NoError(t, f.database.Global().QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL FROM global_oauth_grants WHERE id = $1`, id).Scan(&revoked))
	return revoked
}

func (f *supervisorFixture) holdsLease(t *testing.T, a *agent.Agent) bool {
	t.Helper()
	_, err := f.tickets.LeaseFor(f.ctx, a.ID)
	if err == nil {
		return true
	}
	require.ErrorIs(t, err, ticket.ErrNoLease)
	return false
}

func (f *supervisorFixture) claim(t *testing.T, a *agent.Agent) {
	t.Helper()
	tk, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "t", Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, a.ID, &tk.ID)
	require.NoError(t, err)
	require.True(t, f.holdsLease(t, a))
}

func (f *supervisorFixture) get(t *testing.T, a *agent.Agent) *agent.Agent {
	t.Helper()
	got, err := f.agents.Get(f.ctx, a.ID)
	require.NoError(t, err)
	return got
}

func TestCreateSlotMakesAWorkerOwnedByTheSupervisor(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 3)

	slot := f.slot(t, sup, "  slot-1 ")

	assert.Equal(t, agent.KindWorker, slot.Kind, "a supervisor creates only workers")
	assert.Equal(t, "slot-1", slot.Name)
	require.NotNil(t, slot.SupervisorID)
	assert.Equal(t, sup.ID, *slot.SupervisorID)
	assert.Nil(t, slot.SlotCap)
	assert.Nil(t, slot.DisabledAt)
	assert.Equal(t, agent.KindSupervisor, sup.Kind)
	require.NotNil(t, sup.SlotCap)
	assert.Equal(t, 3, *sup.SlotCap)
}

func TestCreateSlotLogsAnAuditLine(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 3)
	slot := f.slot(t, sup, "slot-1")

	lines := f.logs.audit(t, "supervisor_slot_created")
	require.Len(t, lines, 1)
	assert.Equal(t, sup.ID.String(), lines[0]["supervisor"])
	assert.Equal(t, slot.ID.String(), lines[0]["slot"])
	assert.Equal(t, f.tenant.String(), lines[0]["tenant"])
}

func TestCreateSlotRefusesWhoIsNotALiveSupervisor(t *testing.T) {
	f := newSupervisorFixture(t)
	worker, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)
	console := f.console(t, "console")
	_, err = f.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'member')`, uuid.New(), f.tenant, f.user.ID)
	require.NoError(t, err)
	conductor, err := agent.NewPostgresStore(f.database).CreateConductor(f.ctx, uuid.New(), "conductor", f.user.ID, nil)
	require.NoError(t, err)

	for name, caller := range map[string]*agent.Agent{"worker": worker, "console": console, "conductor": conductor} {
		t.Run(name, func(t *testing.T) {
			_, err := f.agents.CreateSlot(f.ctx, caller, "slot")
			assert.ErrorIs(t, err, agent.ErrNotSupervisor)
		})
	}

	t.Run("unknown agent", func(t *testing.T) {
		_, err := f.agents.CreateSlot(f.ctx, &agent.Agent{ID: uuid.New()}, "slot")
		assert.ErrorIs(t, err, agent.ErrNotFound)
	})
	t.Run("another tenant's supervisor", func(t *testing.T) {
		otherCtx := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
		other := f.supervisorIn(t, otherCtx, "sup", 2)
		_, err := f.agents.CreateSlot(f.ctx, other, "slot")
		assert.ErrorIs(t, err, agent.ErrNotFound)
	})
	t.Run("empty name", func(t *testing.T) {
		sup := f.supervisor(t, "sup-empty", 2)
		_, err := f.agents.CreateSlot(f.ctx, sup, "  ")
		assert.ErrorIs(t, err, agent.ErrEmptyName)
	})
	t.Run("disabled supervisor", func(t *testing.T) {
		sup := f.supervisor(t, "sup-off", 2)
		_, err := f.agents.SetSupervisorDisabled(f.ctx, sup.ID, true)
		require.NoError(t, err)
		// The caller's copy is stale and says enabled: the database decides.
		_, err = f.agents.CreateSlot(f.ctx, sup, "slot")
		assert.ErrorIs(t, err, agent.ErrIsDisabled)
	})
	t.Run("archived supervisor", func(t *testing.T) {
		sup := f.supervisor(t, "sup-archived", 2)
		_, err := f.agents.SetSupervisorDisabled(f.ctx, sup.ID, true)
		require.NoError(t, err)
		_, err = f.database.Exec(f.ctx, `UPDATE agents SET archived_at = now() WHERE id = $1`, sup.ID)
		require.NoError(t, err)
		_, err = f.agents.CreateSlot(f.ctx, sup, "slot")
		assert.ErrorIs(t, err, agent.ErrArchived)
	})
}

func TestCreateSlotNameTaken(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 3)
	_, err := f.agents.Create(f.ctx, "taken")
	require.NoError(t, err)

	_, err = f.agents.CreateSlot(f.ctx, sup, "taken")
	assert.ErrorIs(t, err, agent.ErrNameTaken)
	// The failed insert did not use up a place.
	f.slot(t, sup, "a")
	f.slot(t, sup, "b")
	f.slot(t, sup, "c")
}

func TestCreateSlotStopsAtTheCapAndRetiringFreesAPlace(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 2)
	first := f.slot(t, sup, "a")
	second := f.slot(t, sup, "b")

	_, err := f.agents.CreateSlot(f.ctx, sup, "c")
	require.ErrorIs(t, err, agent.ErrSlotCapReached)

	_, err = f.agents.SetDisabled(f.ctx, second.ID, true)
	require.NoError(t, err)
	_, err = f.agents.CreateSlot(f.ctx, sup, "c")
	require.ErrorIs(t, err, agent.ErrSlotCapReached, "a disabled slot still holds its place until retired")

	_, err = f.agents.RetireSlot(f.ctx, sup, first.ID)
	require.NoError(t, err)
	f.slot(t, sup, "c")
	_, err = f.agents.CreateSlot(f.ctx, sup, "d")
	assert.ErrorIs(t, err, agent.ErrSlotCapReached)
}

func TestCapIsPerSupervisor(t *testing.T) {
	f := newSupervisorFixture(t)
	one, two := f.supervisor(t, "one", 1), f.supervisor(t, "two", 1)
	f.slot(t, one, "a")
	f.slot(t, two, "b")
}

// The cap is checked under a lock on the supervisor's row: without it every
// racer counts the same live slots and all of them insert. A trigger that
// stalls each insert holds the gap between the count and the insert open, so
// the unlocked version oversubscribes every time rather than when the
// scheduler happens to interleave.
func TestConcurrentCreatesCannotExceedTheCap(t *testing.T) {
	app, owner := dbtest.WithOwnerDB(t)
	tenant := dbtest.NewTenant(t, app)
	base := &consoleFixture{database: app, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant)}
	base.pending = pendingaction.NewService(pendingaction.NewPostgresStore(app), pendingaction.Config{}, slog.DiscardHandler)
	base.tickets = ticket.NewService(ticket.NewPostgresStore(app), slog.DiscardHandler)
	f := withLogs(t, base)
	_, err := owner.Exec(base.ctx, `
		CREATE FUNCTION stall_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.3); RETURN NEW; END $$;
		CREATE TRIGGER stall_racers BEFORE INSERT ON agents FOR EACH ROW WHEN (NEW.name LIKE 'racer-%') EXECUTE FUNCTION stall_insert();`)
	require.NoError(t, err)

	const slotCap, existing, racers = 4, 1, 8
	sup := f.supervisor(t, "sup", slotCap)
	f.slot(t, sup, "existing")

	start := make(chan struct{})
	results := make(chan error, racers)
	for i := range racers {
		go func() {
			<-start
			_, err := f.agents.CreateSlot(f.ctx, sup, fmt.Sprintf("racer-%d", i))
			results <- err
		}()
	}
	close(start)

	created, refused := 0, 0
	for range racers {
		switch err := <-results; err {
		case nil:
			created++
		default:
			require.ErrorIs(t, err, agent.ErrSlotCapReached)
			refused++
		}
	}
	assert.Equal(t, slotCap-existing, created)
	assert.Equal(t, racers-(slotCap-existing), refused)
	slots, err := agent.NewPostgresStore(app).ListSlots(f.ctx, sup.ID, true)
	require.NoError(t, err)
	assert.Len(t, slots, slotCap)
}

func TestRetireSlotEndsEverythingTheSlotHad(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 3)
	slot := f.slot(t, sup, "slot")
	grant := f.grant(t, slot)
	f.claim(t, slot)

	retired, err := f.agents.RetireSlot(f.ctx, sup, slot.ID)
	require.NoError(t, err)

	assert.NotNil(t, retired.DisabledAt)
	assert.NotNil(t, retired.ArchivedAt)
	assert.True(t, f.grantRevoked(t, grant), "grants revoked")
	assert.False(t, f.holdsLease(t, slot), "lease released")
	lines := f.logs.audit(t, "supervisor_slot_retired")
	require.Len(t, lines, 1)
	assert.Equal(t, sup.ID.String(), lines[0]["supervisor"])
	assert.Equal(t, slot.ID.String(), lines[0]["slot"])
	assert.Equal(t, f.tenant.String(), lines[0]["tenant"])
}

func TestCreateSlotRevivesItsOwnRetiredSlotThatNeverDidAnything(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 1)
	first := f.slot(t, sup, "slot")
	_, err := f.agents.RetireSlot(f.ctx, sup, first.ID)
	require.NoError(t, err)

	again, err := f.agents.CreateSlot(f.ctx, sup, "slot")
	require.NoError(t, err)

	assert.Equal(t, first.ID, again.ID, "the same agent, brought back")
	assert.Nil(t, again.ArchivedAt)
	assert.Nil(t, again.DisabledAt)
	live, err := f.agents.Slots(f.ctx, sup, false)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, "slot", live[0].Name)
}

func TestCreateSlotRevivingStillRespectsTheCap(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 1)
	first := f.slot(t, sup, "old")
	_, err := f.agents.RetireSlot(f.ctx, sup, first.ID)
	require.NoError(t, err)
	f.slot(t, sup, "new")

	_, err = f.agents.CreateSlot(f.ctx, sup, "old")
	assert.ErrorIs(t, err, agent.ErrSlotCapReached)
	assert.NotNil(t, f.get(t, first).ArchivedAt)
}

func TestCreateSlotKeepsTheNameOfARetiredSlotWithHistory(t *testing.T) {
	for name, work := range map[string]func(f *supervisorFixture, t *testing.T, slot *agent.Agent){
		"a lease": func(f *supervisorFixture, t *testing.T, slot *agent.Agent) { f.claim(t, slot) },
		"a comment": func(f *supervisorFixture, t *testing.T, slot *agent.Agent) {
			tk, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "t", Status: ticket.StatusOpen})
			require.NoError(t, err)
			_, err = f.tickets.AddComment(f.ctx, tk.ID, ticket.Author{AgentID: &slot.ID}, "hello")
			require.NoError(t, err)
		},
		"a ticket it filed": func(f *supervisorFixture, t *testing.T, slot *agent.Agent) {
			_, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "t", Status: ticket.StatusOpen, CreatedByAgentID: &slot.ID})
			require.NoError(t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSupervisorFixture(t)
			sup := f.supervisor(t, "sup", 3)
			worked := f.slot(t, sup, "worked")
			work(f, t, worked)
			_, err := f.agents.RetireSlot(f.ctx, sup, worked.ID)
			require.NoError(t, err)

			_, err = f.agents.CreateSlot(f.ctx, sup, "worked")
			assert.ErrorIs(t, err, agent.ErrNameRetired)
			assert.NotNil(t, f.get(t, worked).ArchivedAt, "left retired")
		})
	}
}

func TestCreateSlotDoesNotReviveAReplacedSlot(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 3)
	old := f.slot(t, sup, "old")
	_, err := f.agents.RetireSlot(f.ctx, sup, old.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `INSERT INTO agents (id, name, kind, replaces_agent_id) VALUES (gen_random_uuid(), 'successor', 'worker', $1)`, old.ID)
	require.NoError(t, err)

	_, err = f.agents.CreateSlot(f.ctx, sup, "old")
	assert.ErrorIs(t, err, agent.ErrNameTaken)
	assert.NotNil(t, f.get(t, old).ArchivedAt)
}

func TestCreateSlotNeverTakesOverSomeoneElsesAgent(t *testing.T) {
	f := newSupervisorFixture(t)
	mine, theirs := f.supervisor(t, "mine", 3), f.supervisor(t, "theirs", 3)
	theirSlot := f.slot(t, theirs, "theirs-slot")
	_, err := f.agents.RetireSlot(f.ctx, theirs, theirSlot.ID)
	require.NoError(t, err)
	plain, err := f.agents.Create(f.ctx, "plain")
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE agents SET disabled_at = now(), archived_at = now() WHERE id = $1`, plain.ID)
	require.NoError(t, err)

	for _, name := range []string{"theirs-slot", "plain", "theirs", "mine"} {
		_, err := f.agents.CreateSlot(f.ctx, mine, name)
		assert.ErrorIs(t, err, agent.ErrNameTaken, name)
	}
	assert.NotNil(t, f.get(t, theirSlot).ArchivedAt)
	assert.NotNil(t, f.get(t, plain).ArchivedAt)
}

func TestRetireSlotIsIdempotent(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 3)
	slot := f.slot(t, sup, "slot")

	first, err := f.agents.RetireSlot(f.ctx, sup, slot.ID)
	require.NoError(t, err)
	again, err := f.agents.RetireSlot(f.ctx, sup, slot.ID)
	require.NoError(t, err)
	assert.True(t, first.ArchivedAt.Equal(*again.ArchivedAt), "keeps the first retire time")
}

func TestRetireSlotReadsOthersAsNotFound(t *testing.T) {
	f := newSupervisorFixture(t)
	mine, theirs := f.supervisor(t, "mine", 3), f.supervisor(t, "theirs", 3)
	theirSlot := f.slot(t, theirs, "theirs-slot")
	plain, err := f.agents.Create(f.ctx, "plain")
	require.NoError(t, err)
	grant := f.grant(t, theirSlot)

	for name, id := range map[string]uuid.UUID{
		"another supervisor's slot": theirSlot.ID,
		"a plain worker":            plain.ID,
		"the supervisor itself":     mine.ID,
		"nothing":                   uuid.New(),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.agents.RetireSlot(f.ctx, mine, id)
			assert.ErrorIs(t, err, agent.ErrNotFound)
		})
	}
	assert.Nil(t, f.get(t, theirSlot).DisabledAt, "untouched")
	assert.False(t, f.grantRevoked(t, grant))
	assert.Nil(t, f.get(t, plain).DisabledAt)
	assert.Nil(t, f.get(t, mine).DisabledAt)
}

func TestRetireSlotAcrossTenantsIsNotFound(t *testing.T) {
	f := newSupervisorFixture(t)
	otherCtx := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	other := f.supervisorIn(t, otherCtx, "sup", 3)
	slot, err := f.agents.CreateSlot(otherCtx, other, "slot")
	require.NoError(t, err)

	mine := f.supervisor(t, "mine", 3)
	_, err = f.agents.RetireSlot(f.ctx, mine, slot.ID)
	assert.ErrorIs(t, err, agent.ErrNotFound)
}

// everything a disabled supervisor takes with it, for each way it can be disabled.
func TestDisablingASupervisorDisablesItsSlots(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 4)
	other := f.supervisor(t, "other", 4)
	live, retired := f.slot(t, sup, "live"), f.slot(t, sup, "retired")
	_, err := f.agents.RetireSlot(f.ctx, sup, retired.ID)
	require.NoError(t, err)
	otherSlot := f.slot(t, other, "other-slot")
	plain, err := f.agents.Create(f.ctx, "plain")
	require.NoError(t, err)
	liveGrant, supGrant, otherGrant := f.grant(t, live), f.grant(t, sup), f.grant(t, otherSlot)
	f.claim(t, live)
	f.claim(t, otherSlot)
	retiredAt := f.get(t, retired).DisabledAt

	_, err = f.agents.SetSupervisorDisabled(f.ctx, sup.ID, true)
	require.NoError(t, err)

	assert.NotNil(t, f.get(t, live).DisabledAt)
	assert.True(t, f.grantRevoked(t, liveGrant), "slot grants revoked")
	assert.True(t, f.grantRevoked(t, supGrant), "supervisor grants revoked")
	assert.False(t, f.holdsLease(t, live), "slot lease released")
	assert.Equal(t, retiredAt, f.get(t, retired).DisabledAt, "an already retired slot is left as it was")
	assert.Nil(t, f.get(t, otherSlot).DisabledAt, "another supervisor's slot")
	assert.False(t, f.grantRevoked(t, otherGrant))
	assert.True(t, f.holdsLease(t, otherSlot))
	assert.Nil(t, f.get(t, plain).DisabledAt)

	lines := f.logs.audit(t, "supervisor_slot_disabled")
	require.Len(t, lines, 1)
	assert.Equal(t, live.ID.String(), lines[0]["slot"])
	assert.Equal(t, sup.ID.String(), lines[0]["supervisor"])
	assert.Equal(t, f.tenant.String(), lines[0]["tenant"])
}

func TestEnablingASupervisorAgainDoesNotEnableItsSlots(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 2)
	slot := f.slot(t, sup, "slot")
	_, err := f.agents.SetSupervisorDisabled(f.ctx, sup.ID, true)
	require.NoError(t, err)

	back, err := f.agents.SetSupervisorDisabled(f.ctx, sup.ID, false)
	require.NoError(t, err)

	assert.Nil(t, back.DisabledAt)
	assert.NotNil(t, f.get(t, slot).DisabledAt)
	_, err = f.agents.CreateSlot(f.ctx, sup, "new")
	assert.NoError(t, err, "the supervisor itself works again")
}

// Whatever disables a supervisor -- here the database function every path
// shares, as tenant member removal calls it -- takes the slots with it.
func TestTheDatabaseDisableTakesTheSlotsWithIt(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 2)
	slot := f.slot(t, sup, "slot")
	grant := f.grant(t, slot)

	_, err := f.database.Exec(f.ctx, `SELECT disable_agent($1, 'owner left the tenant')`, sup.ID)
	require.NoError(t, err)

	assert.NotNil(t, f.get(t, slot).DisabledAt)
	assert.True(t, f.grantRevoked(t, grant))
}

func TestLastGrantEndingDisablesTheSlotsButNotTheSupervisor(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 3)
	other := f.supervisor(t, "other", 3)
	live, retired := f.slot(t, sup, "live"), f.slot(t, sup, "retired")
	_, err := f.agents.RetireSlot(f.ctx, sup, retired.ID)
	require.NoError(t, err)
	otherSlot := f.slot(t, other, "other-slot")
	grant := f.grant(t, live)
	f.claim(t, live)
	f.claim(t, otherSlot)

	require.NoError(t, f.agents.GrantsEnded(context.Background(), f.tenant, sup.ID),
		"the caller's context need not be scoped to the tenant")

	assert.NotNil(t, f.get(t, live).DisabledAt)
	assert.True(t, f.grantRevoked(t, grant))
	assert.False(t, f.holdsLease(t, live))
	assert.Nil(t, f.get(t, sup).DisabledAt, "the supervisor itself stays enabled")
	assert.Nil(t, f.get(t, otherSlot).DisabledAt)
	assert.True(t, f.holdsLease(t, otherSlot))
	require.Len(t, f.logs.audit(t, "supervisor_slot_disabled"), 1)

	require.NoError(t, f.agents.GrantsEnded(context.Background(), f.tenant, sup.ID), "idempotent")
	require.Len(t, f.logs.audit(t, "supervisor_slot_disabled"), 1, "nothing more to disable")
}

func TestGrantsEndingForAnythingElseDisablesNothing(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 2)
	slot := f.slot(t, sup, "slot")

	require.NoError(t, f.agents.GrantsEnded(context.Background(), f.tenant, slot.ID), "a slot is not a supervisor")
	require.NoError(t, f.agents.GrantsEnded(context.Background(), f.tenant, uuid.New()), "an unknown agent")
	assert.Nil(t, f.get(t, slot).DisabledAt)
}

func TestSupervisorsAreInNeitherAgentList(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 2)
	slot := f.slot(t, sup, "slot")
	_, err := f.agents.RetireSlot(f.ctx, sup, slot.ID)
	require.NoError(t, err)
	live := f.slot(t, sup, "live")
	_, err = f.agents.SetSupervisorDisabled(f.ctx, sup.ID, true)
	require.NoError(t, err)

	for _, includeArchived := range []bool{false, true} {
		workers, err := f.agents.List(f.ctx, includeArchived)
		require.NoError(t, err)
		for _, w := range workers {
			assert.NotEqual(t, sup.ID, w.ID, "supervisor in the worker list")
		}
		conductors, err := agent.NewPostgresStore(f.database).ListConductors(f.ctx, nil, includeArchived)
		require.NoError(t, err)
		assert.Empty(t, conductors)
	}

	workers, err := f.agents.List(f.ctx, false)
	require.NoError(t, err)
	require.Len(t, workers, 1)
	assert.Equal(t, live.ID, workers[0].ID, "slots list as workers")
	require.NotNil(t, workers[0].SupervisorID)
	assert.Equal(t, sup.ID, *workers[0].SupervisorID)
}

func TestTheDatabaseGuardsTheSupervisorShape(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 2)
	worker, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)
	slot := f.slot(t, sup, "slot")
	otherCtx := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	otherSup := f.supervisorIn(t, otherCtx, "other-sup", 2)

	for name, c := range map[string]struct {
		query string
		args  []any
		want  string
	}{
		"supervisor without a cap":           {`INSERT INTO agents (id, name, kind) VALUES ($1, 'x', 'supervisor')`, []any{uuid.New()}, "agents_slot_cap_iff_supervisor"},
		"cap of zero":                        {`INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, 'x', 'supervisor', 0)`, []any{uuid.New()}, "agents_slot_cap_range"},
		"cap above the maximum":              {`INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, 'x', 'supervisor', $2)`, []any{uuid.New(), agent.MaxSlotCap + 1}, "agents_slot_cap_range"},
		"a worker with a cap":                {`INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, 'x', 'worker', 2)`, []any{uuid.New()}, "agents_slot_cap_iff_supervisor"},
		"a console slot":                     {`INSERT INTO agents (id, name, kind, supervisor_agent_id) VALUES ($1, 'x', 'console', $2)`, []any{uuid.New(), sup.ID}, "agents_slot_is_worker"},
		"a slot of a plain worker":           {`INSERT INTO agents (id, name, kind, supervisor_agent_id) VALUES ($1, 'x', 'worker', $2)`, []any{uuid.New(), worker.ID}, "must be a supervisor"},
		"a slot of another tenant's":         {`INSERT INTO agents (id, name, kind, supervisor_agent_id) VALUES ($1, 'x', 'worker', $2)`, []any{uuid.New(), otherSup.ID}, "must be a supervisor"},
		"a slot moves to another supervisor": {`UPDATE agents SET supervisor_agent_id = $2 WHERE id = $1`, []any{slot.ID, otherSup.ID}, "cannot change"},
		"a worker becomes a plain worker's":  {`UPDATE agents SET supervisor_agent_id = $2 WHERE id = $1`, []any{worker.ID, worker.ID}, "must be a supervisor"},
		"a worker becomes another tenant's":  {`UPDATE agents SET supervisor_agent_id = $2 WHERE id = $1`, []any{worker.ID, otherSup.ID}, "must be a supervisor"},
		"a slot is cut loose":                {`UPDATE agents SET supervisor_agent_id = NULL WHERE id = $1`, []any{slot.ID}, "cannot change"},
		"a supervisor turns into a worker":   {`UPDATE agents SET kind = 'worker', slot_cap = NULL WHERE id = $1`, []any{sup.ID}, "cannot change"},
		"a worker turns into a supervisor":   {`UPDATE agents SET kind = 'supervisor', slot_cap = 2 WHERE id = $1`, []any{worker.ID}, "cannot change"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.database.Exec(f.ctx, c.query, c.args...)
			assert.ErrorContains(t, err, c.want)
		})
	}

	t.Run("the maximum cap is accepted", func(t *testing.T) {
		_, err := f.database.Exec(f.ctx, `INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, 'max', 'supervisor', $2)`, uuid.New(), agent.MaxSlotCap)
		assert.NoError(t, err)
	})
}

func TestSlotListsHideRetiredSlotsUnlessAsked(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 3)
	store := agent.NewPostgresStore(f.database)
	live, retired := f.slot(t, sup, "a-live"), f.slot(t, sup, "b-retired")
	_, err := f.agents.RetireSlot(f.ctx, sup, retired.ID)
	require.NoError(t, err)

	names := func(includeArchived bool) []string {
		slots, err := store.ListSlots(f.ctx, sup.ID, includeArchived)
		require.NoError(t, err)
		var out []string
		for _, s := range slots {
			out = append(out, s.Name)
		}
		return out
	}
	assert.Equal(t, []string{live.Name}, names(false))
	assert.Equal(t, []string{"a-live", "b-retired"}, names(true))
}

// A supervisor is not a worker to the routes that take an agent id, and not
// one anybody can sign in as by picking it on the consent page.
func TestASupervisorIsNotReachableAsAWorker(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 2)

	_, err := f.agents.GetWorker(f.ctx, sup.ID)
	assert.ErrorIs(t, err, agent.ErrNotFound)
	_, err = f.agents.SetDisabled(f.ctx, sup.ID, true)
	assert.ErrorIs(t, err, agent.ErrNotFound)
	_, err = f.agents.SignOutWorkspaces(f.ctx, sup.ID)
	assert.ErrorIs(t, err, agent.ErrNotFound)

	_, err = f.agents.Choose(f.ctx, f.user, &sup.ID, "", "")
	assert.ErrorIs(t, err, oauth.ErrAgentUnavailable)
	_, err = f.agents.ChooseReplacing(f.ctx, f.user, sup.ID, "new", "")
	assert.ErrorIs(t, err, oauth.ErrReplaceUnavailable)
	_, err = f.agents.CreateKind(f.ctx, "sup2", agent.KindSupervisor)
	assert.ErrorIs(t, err, agent.ErrBadKind)

	_, err = f.agents.SetSupervisorDisabled(f.ctx, uuid.New(), true)
	assert.ErrorIs(t, err, agent.ErrNotFound)
	worker, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)
	_, err = f.agents.SetSupervisorDisabled(f.ctx, worker.ID, true)
	assert.ErrorIs(t, err, agent.ErrNotFound, "only a supervisor")
	assert.Nil(t, f.get(t, sup).DisabledAt)
}

func TestAdoptSlotLinksAWorkerWithNoSupervisor(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 2)
	reused, err := f.agents.Create(f.ctx, "pilot-1")
	require.NoError(t, err)

	adopted, err := f.agents.AdoptSlot(f.ctx, sup, " pilot-1 ")
	require.NoError(t, err)

	assert.Equal(t, reused.ID, adopted.ID)
	require.NotNil(t, adopted.SupervisorID)
	assert.Equal(t, sup.ID, *adopted.SupervisorID)
	live, err := f.agents.Slots(f.ctx, sup, false)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, reused.ID, live[0].ID)
	assert.Len(t, f.logs.audit(t, "supervisor_slot_adopted"), 1)

	again, err := f.agents.AdoptSlot(f.ctx, sup, "pilot-1")
	require.NoError(t, err, "adopting a slot it already has is harmless")
	assert.Equal(t, reused.ID, again.ID)
	assert.Len(t, f.logs.audit(t, "supervisor_slot_adopted"), 2)
}

func TestAdoptSlotRefusals(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 5)
	other := f.supervisor(t, "other", 5)
	f.slot(t, other, "theirs")
	archived, err := f.agents.Create(f.ctx, "gone")
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE agents SET disabled_at = now(), archived_at = now() WHERE id = $1`, archived.ID)
	require.NoError(t, err)
	_, err = f.agents.CreateKind(f.ctx, "a-console", agent.KindConsole)
	require.NoError(t, err)

	_, err = f.agents.AdoptSlot(f.ctx, sup, "nobody")
	assert.ErrorIs(t, err, agent.ErrNotFound)
	_, err = f.agents.AdoptSlot(f.ctx, sup, "theirs")
	assert.ErrorIs(t, err, agent.ErrNotFound, "another supervisor's slot is not adoptable and not revealed")
	_, err = f.agents.AdoptSlot(f.ctx, sup, "gone")
	assert.ErrorIs(t, err, agent.ErrArchived)
	_, err = f.agents.AdoptSlot(f.ctx, sup, "a-console")
	assert.ErrorIs(t, err, agent.ErrNotAdoptable)
	_, err = f.agents.AdoptSlot(f.ctx, sup, " ")
	assert.ErrorIs(t, err, agent.ErrEmptyName)
}

func TestAdoptSlotRespectsTheCapButNotForItsOwnSlot(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 1)
	_, err := f.agents.Create(f.ctx, "reused")
	require.NoError(t, err)
	own := f.slot(t, sup, "own")

	_, err = f.agents.AdoptSlot(f.ctx, sup, "reused")
	require.ErrorIs(t, err, agent.ErrSlotCapReached)
	again, err := f.agents.AdoptSlot(f.ctx, sup, "own")
	require.NoError(t, err)
	assert.Equal(t, own.ID, again.ID)

	_, err = f.agents.RetireSlot(f.ctx, sup, own.ID)
	require.NoError(t, err)
	_, err = f.agents.AdoptSlot(f.ctx, sup, "reused")
	assert.NoError(t, err, "retiring freed the place")
}

func TestAdoptedSlotStaysLinkedToItsSupervisor(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 2)
	other := f.supervisor(t, "other", 2)
	reused, err := f.agents.Create(f.ctx, "reused")
	require.NoError(t, err)
	_, err = f.agents.AdoptSlot(f.ctx, sup, "reused")
	require.NoError(t, err)

	_, err = f.database.Exec(f.ctx, `UPDATE agents SET supervisor_agent_id = $1 WHERE id = $2`, other.ID, reused.ID)
	assert.Error(t, err, "the link is immutable once made")
	_, err = f.database.Exec(f.ctx, `UPDATE agents SET supervisor_agent_id = $1 WHERE name = 'sup'`, sup.ID)
	assert.Error(t, err, "a supervisor is never a slot")
}

func TestConcurrentAdoptionsCannotExceedTheCap(t *testing.T) {
	app, owner := dbtest.WithOwnerDB(t)
	tenant := dbtest.NewTenant(t, app)
	base := &consoleFixture{database: app, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant)}
	base.pending = pendingaction.NewService(pendingaction.NewPostgresStore(app), pendingaction.Config{}, slog.DiscardHandler)
	base.tickets = ticket.NewService(ticket.NewPostgresStore(app), slog.DiscardHandler)
	f := withLogs(t, base)
	_, err := owner.Exec(base.ctx, `
		CREATE FUNCTION stall_link() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.3); RETURN NEW; END $$;
		CREATE TRIGGER stall_adopters BEFORE UPDATE OF supervisor_agent_id ON agents FOR EACH ROW EXECUTE FUNCTION stall_link();`)
	require.NoError(t, err)

	const slotCap, existing, racers = 4, 1, 8
	sup := f.supervisor(t, "sup", slotCap)
	f.slot(t, sup, "existing")
	for i := range racers {
		_, err := f.agents.Create(f.ctx, fmt.Sprintf("racer-%d", i))
		require.NoError(t, err)
	}

	start := make(chan struct{})
	results := make(chan error, racers)
	for i := range racers {
		go func() {
			<-start
			_, err := f.agents.AdoptSlot(f.ctx, sup, fmt.Sprintf("racer-%d", i))
			results <- err
		}()
	}
	close(start)

	adopted := 0
	for range racers {
		if err := <-results; err == nil {
			adopted++
		} else {
			require.ErrorIs(t, err, agent.ErrSlotCapReached)
		}
	}
	assert.Equal(t, slotCap-existing, adopted)
	slots, err := agent.NewPostgresStore(app).ListSlots(f.ctx, sup.ID, true)
	require.NoError(t, err)
	assert.Len(t, slots, slotCap)
}

func (f *supervisorFixture) backdate(t *testing.T, query string, args ...any) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, query, args...)
	require.NoError(t, err)
}

func TestSupervisorLastSeenIsTheNewestOfCreationPollAndSlotReports(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 4)
	slot := f.slot(t, sup, "slot-a")
	now := time.Now()
	store := agent.NewPostgresStore(f.database)

	// Never reported: it counts from its creation.
	seen, err := store.SupervisorLastSeen(f.ctx, sup.ID)
	require.NoError(t, err)
	assert.WithinDuration(t, sup.CreatedAt, seen, time.Second)

	f.backdate(t, `UPDATE agents SET created_at = $2 WHERE id = $1`, sup.ID, now.Add(-72*time.Hour))
	poll := now.Add(-48 * time.Hour)
	f.backdate(t, `INSERT INTO worker_polls (agent_id, protocol, last_polled_at) VALUES ($1, 1, $2)`, sup.ID, poll)
	seen, err = store.SupervisorLastSeen(f.ctx, sup.ID)
	require.NoError(t, err)
	assert.WithinDuration(t, poll, seen, time.Second)

	report := now.Add(-30 * time.Hour)
	f.backdate(t, `INSERT INTO supervisor_slot_states (tenant_id, agent_id, phase, phase_entered_at, reported_at) VALUES ($1, $2, 'idle', $3, $3)`,
		f.tenant, slot.ID, report)
	older := f.slot(t, sup, "slot-b")
	f.backdate(t, `INSERT INTO supervisor_slot_states (tenant_id, agent_id, phase, phase_entered_at, reported_at) VALUES ($1, $2, 'idle', $3, $3)`,
		f.tenant, older.ID, now.Add(-40*time.Hour))
	seen, err = store.SupervisorLastSeen(f.ctx, sup.ID)
	require.NoError(t, err)
	assert.WithinDuration(t, report, seen, time.Second, "the newest report wins, not the last one written")

	newerPoll := now.Add(-time.Hour)
	f.backdate(t, `UPDATE worker_polls SET last_polled_at = $2 WHERE agent_id = $1`, sup.ID, newerPoll)
	seen, err = store.SupervisorLastSeen(f.ctx, sup.ID)
	require.NoError(t, err)
	assert.WithinDuration(t, newerPoll, seen, time.Second)

	_, err = store.SupervisorLastSeen(f.ctx, slot.ID)
	assert.ErrorIs(t, err, agent.ErrNotFound, "a slot is not a supervisor")
}

func TestSupervisorIsStaleOnlyPastTwentyFourHours(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 4)
	created := sup.CreatedAt
	for _, tc := range []struct {
		name  string
		after time.Duration
		stale bool
	}{
		{"just under", agent.SupervisorStaleAfter - time.Nanosecond, false},
		{"exactly", agent.SupervisorStaleAfter, false},
		{"just over", agent.SupervisorStaleAfter + time.Nanosecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen, stale, err := f.agents.SupervisorLastSeen(f.ctx, sup.ID, created.Add(tc.after))
			require.NoError(t, err)
			assert.Equal(t, tc.stale, stale)
			assert.WithinDuration(t, created, seen, time.Second)
		})
	}
}

func TestDisablingASupervisorWritesAnAuditLine(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 4)
	_, err := f.agents.SetSupervisorDisabled(f.ctx, sup.ID, true)
	require.NoError(t, err)
	lines := f.logs.audit(t, "supervisor_disabled_set")
	require.Len(t, lines, 1)
	assert.Equal(t, sup.ID.String(), lines[0]["supervisor"])
	assert.Equal(t, true, lines[0]["disabled"])

	_, err = f.agents.SetSupervisorDisabled(f.ctx, sup.ID, false)
	require.NoError(t, err)
	lines = f.logs.audit(t, "supervisor_disabled_set")
	require.Len(t, lines, 2)
	assert.Equal(t, false, lines[1]["disabled"])
}
