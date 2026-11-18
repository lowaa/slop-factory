package agent_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

type supervisorJSON struct {
	SlotCap   int `json:"slot_cap"`
	SlotCount int `json:"slot_count"`
}

func (p *provisioning) capPath() string {
	return "/api/supervisors/" + p.sup.ID.String() + "/slot-cap"
}

func TestRaisingTheCapLetsTheNextSlotThrough(t *testing.T) {
	p := newProvisioning(t)
	capped := p.supervisor(t, "capped", 1)
	p.slot(t, capped, "first")
	_, err := p.svc.CreateSlot(p.ctx, capped, "second")
	require.ErrorIs(t, err, agent.ErrSlotCapReached)

	rec := p.do(http.MethodPatch, "/api/supervisors/"+capped.ID.String()+"/slot-cap", "user", `{"slot_cap":2}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decode[supervisorJSON](t, rec)
	assert.Equal(t, supervisorJSON{SlotCap: 2, SlotCount: 1}, got)
	_, err = p.svc.CreateSlot(p.ctx, capped, "second")
	require.NoError(t, err)

	lines := p.logs.audit(t, "supervisor_slot_cap_changed")
	require.Len(t, lines, 1)
	assert.Equal(t, capped.ID.String(), lines[0]["supervisor"])
	assert.EqualValues(t, 1, lines[0]["old_slot_cap"])
	assert.EqualValues(t, 2, lines[0]["new_slot_cap"])
	assert.Equal(t, p.user.ID.String(), lines[0]["user_id"])
}

func TestLoweringTheCapBelowTheLiveSlotsKeepsThemAndRefusesNewOnes(t *testing.T) {
	p := newProvisioning(t)
	sup := p.supervisor(t, "shrinking", 3)
	for _, name := range []string{"a", "b", "c"} {
		p.slot(t, sup, name)
	}

	rec := p.do(http.MethodPatch, "/api/supervisors/"+sup.ID.String()+"/slot-cap", "user", `{"slot_cap":1}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, supervisorJSON{SlotCap: 1, SlotCount: 3}, decode[supervisorJSON](t, rec), "no slot was retired")
	_, err := p.svc.CreateSlot(p.ctx, sup, "d")
	require.ErrorIs(t, err, agent.ErrSlotCapReached)

	slots, err := p.svc.Slots(p.ctx, sup, false)
	require.NoError(t, err)
	require.Len(t, slots, 3)
	for _, s := range slots {
		assert.Nil(t, s.DisabledAt)
	}
	// Retiring down to the cap is not enough; one place must be free.
	_, err = p.svc.RetireSlot(p.ctx, sup, slots[0].ID)
	require.NoError(t, err)
	_, err = p.svc.RetireSlot(p.ctx, sup, slots[1].ID)
	require.NoError(t, err)
	_, err = p.svc.CreateSlot(p.ctx, sup, "d")
	require.ErrorIs(t, err, agent.ErrSlotCapReached)
	_, err = p.svc.RetireSlot(p.ctx, sup, slots[2].ID)
	require.NoError(t, err)
	p.slot(t, sup, "d")
}

func TestTheCapMustBeInRange(t *testing.T) {
	p := newProvisioning(t)
	for name, body := range map[string]string{
		"zero":          `{"slot_cap":0}`,
		"negative":      `{"slot_cap":-1}`,
		"above the max": `{"slot_cap":17}`,
		"missing":       `{}`,
		"not a number":  `{"slot_cap":"many"}`,
	} {
		rec := p.do(http.MethodPatch, p.capPath(), "user", body)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, name+": "+rec.Body.String())
	}
	got, err := p.svc.Get(p.ctx, p.sup.ID)
	require.NoError(t, err)
	assert.Equal(t, 8, *got.SlotCap)
}

func TestTheCapBoundsAreAccepted(t *testing.T) {
	p := newProvisioning(t)
	for _, c := range []int{1, agent.MaxSlotCap} {
		rec := p.do(http.MethodPatch, p.capPath(), "user", fmt.Sprintf(`{"slot_cap":%d}`, c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, c, decode[supervisorJSON](t, rec).SlotCap)
	}
}

func TestOnlyTheTenantOwnerChangesTheCap(t *testing.T) {
	p := newProvisioning(t)
	worker, err := p.svc.Create(p.ctx, "plain-worker")
	require.NoError(t, err)
	body := `{"slot_cap":9}`

	for who, bearer := range map[string]string{
		"the supervisor itself": p.bearer,
		"a worker":              p.token(t, worker, "agent"),
		"no credential":         "",
	} {
		rec := p.do(http.MethodPatch, p.capPath(), bearer, body)
		assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code, who+" "+rec.Body.String())
	}
	_, err = p.database.Global().Exec(context.Background(),
		`UPDATE global_tenant_memberships SET role = 'member' WHERE user_id = $1`, p.user.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, p.do(http.MethodPatch, p.capPath(), "user", body).Code, "a member is not the owner")

	got, err := p.svc.Get(p.ctx, p.sup.ID)
	require.NoError(t, err)
	assert.Equal(t, 8, *got.SlotCap, "nothing changed")
	assert.Empty(t, p.logs.audit(t, "supervisor_slot_cap_changed"))
}

func TestOnlyALiveSupervisorHasACapToChange(t *testing.T) {
	p := newProvisioning(t)
	worker, err := p.svc.Create(p.ctx, "plain-worker")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, p.do(http.MethodPatch, "/api/supervisors/"+worker.ID.String()+"/slot-cap", "user", `{"slot_cap":2}`).Code)
	assert.Equal(t, http.StatusNotFound, p.do(http.MethodPatch, "/api/supervisors/"+uuid.NewString()+"/slot-cap", "user", `{"slot_cap":2}`).Code)

	gone := p.supervisor(t, "gone", 2)
	_, err = p.svc.SetSupervisorDisabled(p.ctx, gone.ID, true)
	require.NoError(t, err)
	_, err = p.database.Exec(p.ctx, `UPDATE agents SET archived_at = now() WHERE id = $1`, gone.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, p.do(http.MethodPatch, "/api/supervisors/"+gone.ID.String()+"/slot-cap", "user", `{"slot_cap":3}`).Code)
}

// waitForLockWaiter returns once some backend is blocked on a row lock, so
// a test can tell its goroutine reached the lock without sleeping.
func waitForLockWaiter(t *testing.T, p *provisioning) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, p.database.Global().QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting))
		return waiting > 0
	}, 10*time.Second, 10*time.Millisecond)
}

// holdRowLock locks the supervisor's row in a transaction until release is
// closed, running body while it is held.
func holdRowLock(t *testing.T, p *provisioning, statement string, args ...any) (release func()) {
	t.Helper()
	locked, releaseCh, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- p.database.InTx(p.ctx, func(q db.Querier) error {
			if _, err := q.Exec(p.ctx, statement, args...); err != nil {
				return err
			}
			close(locked)
			<-releaseCh
			return nil
		})
	}()
	<-locked
	return func() {
		close(releaseCh)
		require.NoError(t, <-done)
	}
}

// The audit line reports the cap the change replaced, so the read of it must
// wait for a change in flight rather than see the committed value under it.
func TestACapChangeReportsTheCapAConcurrentChangeLeft(t *testing.T) {
	p := newProvisioning(t)
	sup := p.supervisor(t, "serialised", 1)
	release := holdRowLock(t, p, `UPDATE agents SET slot_cap = 2 WHERE id = $1`, sup.ID)

	type result struct {
		previous int
		err      error
	}
	done := make(chan result, 1)
	go func() {
		_, previous, err := agent.NewPostgresStore(p.database).SetSlotCap(p.ctx, sup.ID, 5)
		done <- result{previous, err}
	}()
	waitForLockWaiter(t, p)
	release()

	got := <-done
	require.NoError(t, got.err)
	assert.Equal(t, 2, got.previous)
}

func TestASlotCreateWaitingOnACapRaiseSeesTheNewCap(t *testing.T) {
	p := newProvisioning(t)
	sup := p.supervisor(t, "serialised", 1)
	p.slot(t, sup, "first")
	release := holdRowLock(t, p, `UPDATE agents SET slot_cap = 2 WHERE id = $1`, sup.ID)

	done := make(chan error, 1)
	go func() {
		_, err := p.svc.CreateSlot(p.ctx, sup, "second")
		done <- err
	}()
	waitForLockWaiter(t, p)
	release()

	require.NoError(t, <-done, "the create read the cap after the raise committed")
}
