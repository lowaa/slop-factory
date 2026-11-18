package change_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
)

func TestCloseAttemptChange_ClosesTheAttemptsOwnChangeOnly(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.addSibling(first)
	f.openAs(first, 7, false)
	f.openAs(second, 8, true)

	require.NoError(t, f.svc.CloseAttemptChange(f.ctx, f.tenant, first.ticket.ID, 2))

	closed := f.host.Closed()
	require.Len(t, closed, 1)
	assert.Equal(t, int64(8), closed[0].Change.Number, "the sibling's change (#7) stays open")
	assert.Equal(t, f.tenant, closed[0].Tenant)
}

func TestCloseAttemptChange_NothingToCloseIsSuccess(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.addSibling(first)
	require.NoError(t, f.svc.CloseAttemptChange(f.ctx, f.tenant, first.ticket.ID, second.attempt), "no change opened")

	f.openAs(second, 8, true)
	_, err := f.database.Exec(f.ctx, `UPDATE ticket_changes SET merged_at = now(), merge_sha = 'abc', merged_by_agent_id = $2 WHERE ticket_id = $1 AND attempt_number = 2`, first.ticket.ID, second.agent)
	require.NoError(t, err)
	require.NoError(t, f.svc.CloseAttemptChange(f.ctx, f.tenant, first.ticket.ID, 2))
	assert.Empty(t, f.host.Closed(), "a merged change is not closed")
}

func TestCloseAttemptChange_AHostFailureIsAuditedAndReturned(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.addSibling(first)
	f.openAs(second, 8, true)
	f.host.CloseErr = errors.New("host down")

	err := f.svc.CloseAttemptChange(f.ctx, f.tenant, first.ticket.ID, 2)
	require.Error(t, err)
	failed := f.log.audited(t, audit.ChangeOperationFailed)
	require.Len(t, failed, 1)
	assert.Equal(t, "close_change", failed[0]["operation"])
	assert.Contains(t, failed[0]["err"], "host down")
}

func TestCloseAttemptChange_ATenantMismatchIsRefused(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.addSibling(first)
	f.openAs(second, 8, true)

	require.Error(t, f.svc.CloseAttemptChange(f.ctx, uuid.New(), first.ticket.ID, 2))
	assert.Empty(t, f.host.Closed())
}
