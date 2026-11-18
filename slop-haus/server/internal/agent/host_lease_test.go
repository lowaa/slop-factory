package agent_test

import (
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
)

type hostLease struct {
	*supervisorAPI
	rc     *agent.Agent
	bearer string
}

func newHostLease(t *testing.T) *hostLease {
	t.Helper()
	a := newSupervisorAPI(t, 100)
	rc := a.conductor(t, "rc-host")
	return &hostLease{supervisorAPI: a, rc: rc, bearer: a.token(t, rc, "agent")}
}

func (h *hostLease) call(op, holder string) int {
	return h.callAs(h.bearer, op, holder)
}

func (h *hostLease) callAs(bearer, op, holder string) int {
	path := "/api/agent/host_lease"
	if op != "" {
		path += "/" + op
	}
	return h.do(http.MethodPost, path, bearer, `{"holder":"`+holder+`"}`).Code
}

// moreConductor is a second Remote Control agent; the fixture's conductor helper also adds the tenant membership, once.
func (h *hostLease) moreConductor(t *testing.T, name string) *agent.Agent {
	t.Helper()
	id := uuid.New()
	_, err := h.database.Exec(h.ctx, `INSERT INTO agents (id, name, kind, owner_user_id) VALUES ($1, $2, 'conductor', $3)`, id, name, h.user.ID)
	require.NoError(t, err)
	got, err := h.svc.Get(h.ctx, id)
	require.NoError(t, err)
	return got
}

// setColumn marks the Remote Control agent disabled or archived directly: the service's own switches manage workers only.
func (h *hostLease) setColumn(t *testing.T, column string) {
	t.Helper()
	_, err := h.database.Exec(h.ctx, `UPDATE agents SET `+column+` = now() WHERE id = $1`, h.rc.ID)
	require.NoError(t, err)
}

// expire backdates the lease, which the database's clock would otherwise take a TTL to do.
func (h *hostLease) expire(t *testing.T) {
	t.Helper()
	_, err := h.database.Exec(h.ctx, `UPDATE remote_control_host_leases SET expires_at = now() - interval '1 second' WHERE agent_id = $1`, h.rc.ID)
	require.NoError(t, err)
}

func TestASecondHostIsRefusedWhileTheLeaseIsLive(t *testing.T) {
	h := newHostLease(t)
	assert.Equal(t, http.StatusOK, h.call("", "host-a"))
	assert.Equal(t, http.StatusConflict, h.call("", "host-b"))
	assert.Equal(t, http.StatusOK, h.call("", "host-a"), "the holder acquiring again is a renewal, so a retried acquire is safe")
}

func TestALapsedLeaseCanBeTakenOverAndTheOldHolderCannotRenew(t *testing.T) {
	h := newHostLease(t)
	require.Equal(t, http.StatusOK, h.call("", "host-a"))
	h.expire(t)

	assert.Equal(t, http.StatusConflict, h.call("renew", "host-a"), "a missed renewal is not revived: the host must stop serving")
	assert.Equal(t, http.StatusOK, h.call("", "host-b"))
	assert.Equal(t, http.StatusConflict, h.call("renew", "host-a"))
	assert.Equal(t, http.StatusOK, h.call("renew", "host-b"))
}

func TestOnlyTheHolderRenews(t *testing.T) {
	h := newHostLease(t)
	assert.Equal(t, http.StatusConflict, h.call("renew", "host-a"), "nothing to renew")
	require.Equal(t, http.StatusOK, h.call("", "host-a"))
	assert.Equal(t, http.StatusConflict, h.call("renew", "host-b"))
	assert.Equal(t, http.StatusOK, h.call("renew", "host-a"))
}

func TestRenewalMovesTheExpiryForward(t *testing.T) {
	h := newHostLease(t)
	require.Equal(t, http.StatusOK, h.call("", "host-a"))
	_, err := h.database.Exec(h.ctx, `UPDATE remote_control_host_leases SET expires_at = now() + interval '1 second' WHERE agent_id = $1`, h.rc.ID)
	require.NoError(t, err)

	rec := h.do(http.MethodPost, "/api/agent/host_lease/renew", h.bearer, `{"holder":"host-a"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	body := decode[struct {
		ExpiresAt time.Time `json:"expires_at"`
		TTL       float64   `json:"ttl_seconds"`
	}](t, rec)
	assert.Greater(t, body.TTL, 1.0)
	assert.True(t, body.ExpiresAt.After(time.Now().Add(5*time.Second)))
}

func TestReleaseFreesTheLeaseForTheHolderOnly(t *testing.T) {
	h := newHostLease(t)
	require.Equal(t, http.StatusOK, h.call("", "host-a"))

	assert.Equal(t, http.StatusNoContent, h.call("release", "host-b"))
	assert.Equal(t, http.StatusConflict, h.call("", "host-b"), "another host cannot release a lease it does not hold")

	assert.Equal(t, http.StatusNoContent, h.call("release", "host-a"))
	assert.Equal(t, http.StatusNoContent, h.call("release", "host-a"), "releasing twice is fine")
	assert.Equal(t, http.StatusOK, h.call("", "host-b"))
}

func TestLeasesAreOnePerAgent(t *testing.T) {
	h := newHostLease(t)
	other := h.moreConductor(t, "rc-other")
	require.Equal(t, http.StatusOK, h.call("", "same-holder-id"))
	assert.Equal(t, http.StatusOK, h.callAs(h.token(t, other, "agent"), "", "same-holder-id"))
	assert.Equal(t, http.StatusConflict, h.callAs(h.token(t, other, "agent"), "", "second"))
}

func TestOnlyARemoteControlAgentHasALease(t *testing.T) {
	h := newHostLease(t)
	worker, err := h.svc.Create(h.ctx, "plain-worker")
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, h.callAs(h.token(t, worker, "agent"), "", "w"))

	sup := h.supervisor(t, "box", 3)
	assert.Equal(t, http.StatusForbidden, h.callAs(h.token(t, sup, "agent"), "", "s"), "a supervisor's credential is not an agent's")
	assert.Equal(t, http.StatusUnauthorized, h.callAs("", "", "anon"))
}

func TestADisabledAgentCannotTakeALease(t *testing.T) {
	h := newHostLease(t)
	h.setColumn(t, "disabled_at")
	assert.GreaterOrEqual(t, h.call("", "host-a"), http.StatusBadRequest)
}

func TestAHolderIdIsBounded(t *testing.T) {
	h := newHostLease(t)
	assert.Equal(t, http.StatusUnprocessableEntity, h.call("", ""))
	assert.Equal(t, http.StatusUnprocessableEntity, h.call("", strings.Repeat("x", 101)))
}

// Archiving ends the agent's grants, so over HTTP the credential is refused first; the service holds the line
// for a caller that got past it.
func TestAnArchivedAgentCannotTakeALease(t *testing.T) {
	h := newHostLease(t)
	h.setColumn(t, "disabled_at")
	h.setColumn(t, "archived_at")

	leases := agent.NewHostLeaseService(agent.NewPostgresHostLeaseStore(h.database), h.svc, 0, slog.DiscardHandler)
	_, err := leases.Acquire(h.ctx, h.rc.ID, "host-a")
	assert.ErrorIs(t, err, agent.ErrArchived)
}
