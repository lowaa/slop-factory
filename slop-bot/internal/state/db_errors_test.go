package state_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// A database call that fails is reported with the operation it was part of.
// An already-cancelled context fails a statement deterministically.
func TestDatabaseFailuresAreWrappedWithTheirOperation(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name    string
		call    func(ctx context.Context, f *fixture) error
		wantMsg string
	}{
		{
			name:    "listing live agents",
			call:    func(ctx context.Context, f *fixture) error { _, err := f.Agents.ListLive(ctx); return err },
			wantMsg: "state: listing live agents",
		},
		{
			name:    "reading a setting",
			call:    func(ctx context.Context, f *fixture) error { _, _, err := f.Settings.Autonomy(ctx); return err },
			wantMsg: "state: reading setting",
		},
		{
			name:    "revoking a merge grant",
			call:    func(ctx context.Context, f *fixture) error { return f.MergeGrants.Revoke(ctx, "7") },
			wantMsg: "state: revoking ticket 7's merge grant",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			err := tc.call(cancelled, f)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantMsg)
			assert.ErrorIs(t, err, context.Canceled)
		})
	}
}

func TestSetStateReportsAFailedUpdate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER no_agent_updates BEFORE UPDATE ON agents
		BEGIN SELECT RAISE(ABORT, 'agent updates refused'); END`)
	require.NoError(t, err)

	changed, err := f.Agents.SetState(ctx, &state.Transition{Agent: "a1", To: state.StateReleased})
	require.Error(t, err)
	assert.False(t, changed)
	assert.ErrorContains(t, err, "state: setting agent a1 to "+string(state.StateReleased))
	assert.ErrorContains(t, err, "agent updates refused")
}

func TestCreateReportsAFailedStateHistoryInsert(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER no_history BEFORE INSERT ON agent_state_changes
		BEGIN SELECT RAISE(ABORT, 'history refused'); END`)
	require.NoError(t, err)

	_, err = f.Agents.Create(ctx, newAgent("a1", "1"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "state: recording state change of agent a1")
	assert.ErrorContains(t, err, "history refused")
	// The agent insert is rolled back with the history it could not record.
	assert.Zero(t, f.count(t, "agents"))
}

func TestAddRecoveryReportsAFailedUpdate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.createAgent(t, "a1", "1")
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER no_agent_updates BEFORE UPDATE ON agents
		BEGIN SELECT RAISE(ABORT, 'agent updates refused'); END`)
	require.NoError(t, err)

	n, err := f.Agents.AddRecovery(ctx, "a1")
	require.Error(t, err)
	assert.Zero(t, n)
	assert.ErrorContains(t, err, "state: counting a recovery of agent a1")
	assert.ErrorContains(t, err, "agent updates refused")
}
