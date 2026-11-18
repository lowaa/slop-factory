package prune_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lowaa/slop-factory/go-clock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/prune"
)

// fakeStore records what the service asked for. The real store is covered
// against a database in prune_test.go; what these tests pin is the
// *orchestration* -- which rules run, what happens when one fails, and
// whether the loop behaves. Against a real store with empty tables, a
// RunOnce that silently skipped two of three tables would pass.
//
// Tenants defaults to a single fixed tenant. Multi-tenant iteration itself
// is exercised against a real database in prune_test.go, where "does this
// number really belong to that tenant" is answerable; a fake can only
// count calls.
type fakeStore struct {
	mu sync.Mutex

	tenants []uuid.UUID

	// swept is what each table was swept for, in order.
	swept []*prune.Rule
	// errs fails the sweeps of a table; deleted is what every other returns.
	errs    map[string]error
	deleted int64

	expireCalls int
	expireNow   time.Time
}

var singleFakeTenant = uuid.New()

func (f *fakeStore) Tenants(context.Context) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tenants) == 0 {
		return []uuid.UUID{singleFakeTenant}, nil
	}
	return f.tenants, nil
}

func (f *fakeStore) Sweep(_ context.Context, rule *prune.Rule) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.swept = append(f.swept, rule)
	if err := f.errs[rule.Table]; err != nil {
		return 0, err
	}
	return f.deleted, nil
}

func (f *fakeStore) ExpirePendingActions(_ context.Context, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expireCalls++
	f.expireNow = now
	return nil
}

// rules returns what was swept for a table.
func (f *fakeStore) rules(table string) []*prune.Rule {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*prune.Rule
	for _, r := range f.swept {
		if r.Table == table {
			out = append(out, r)
		}
	}
	return out
}

func fullConfig() prune.Config {
	return prune.Config{
		SessionRetention:  30 * 24 * time.Hour,
		LeaseRetention:    30 * 24 * time.Hour,
		DeliveryRetention: 30 * 24 * time.Hour,
		LLMUsageRetention: 365 * 24 * time.Hour,
		Interval:          time.Hour,
	}
}

func TestRunOnce_PrunesEveryEnabledTable(t *testing.T) {
	store := &fakeStore{deleted: 3}
	svc := prune.NewService(store, fullConfig(), slog.DiscardHandler)

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)

	assert.Len(t, store.rules("sessions"), 1)
	assert.Len(t, store.rules("leases"), 1)
	assert.Len(t, store.rules("github_deliveries"), 1)
	assert.Len(t, store.rules("llm_usage"), 1)
	assert.NotZero(t, result.Sessions)
	assert.NotZero(t, result.Leases)
	assert.NotZero(t, result.LeaseActivity)
	assert.NotZero(t, result.Deliveries)
	assert.NotZero(t, result.LLMUsage)
	assert.NotZero(t, result.Credentials)
	assert.Equal(t, int64(3), result.Sessions, "one rule, three rows")
	assert.False(t, result.Empty())
}

// Credentials are global: however many tenants there are, one pass sweeps
// them once, a grace period after they die.
func TestRunOnce_PrunesCredentialsOncePerPass(t *testing.T) {
	store := &fakeStore{tenants: []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}}
	svc := prune.NewService(store, fullConfig(), slog.DiscardHandler)

	_, err := svc.RunOnce(context.Background())
	require.NoError(t, err)

	tokens := store.rules("global_auth_tokens")
	require.Len(t, tokens, 1)
	assert.True(t, tokens[0].Global)
	assert.Equal(t, prune.ExpiredCredentialGrace, tokens[0].Period)
	clients := store.rules("global_oauth_clients")
	require.Len(t, clients, 1)
	assert.Equal(t, prune.UnclaimedClientGrace, clients[0].Period)
	assert.Equal(t, 3, store.expireCalls, "the per-tenant expiry still runs for each")
}

// Each retention becomes a period of its own. A service that used one
// table's retention for another still deletes *something* -- just the wrong
// something.
func TestRunOnce_DerivesEachPeriodFromItsOwnRetention(t *testing.T) {
	store := &fakeStore{}
	cfg := prune.Config{
		SessionRetention:  100 * time.Hour,
		LeaseRetention:    200 * time.Hour,
		DeliveryRetention: 300 * time.Hour,
		LLMUsageRetention: 400 * time.Hour,
	}
	_, err := prune.NewService(store, cfg, slog.DiscardHandler).RunOnce(context.Background())
	require.NoError(t, err)

	period := func(table string) time.Duration {
		rules := store.rules(table)
		require.NotEmpty(t, rules, table)
		return rules[0].Period
	}
	assert.Equal(t, 100*time.Hour, period("sessions"))
	assert.Equal(t, 200*time.Hour, period("leases"))
	assert.Equal(t, 200*time.Hour, period("lock_holds"), "lease activity rides the lease retention")
	assert.Equal(t, 300*time.Hour, period("github_deliveries"))
	assert.Equal(t, 300*time.Hour, period("intake_deliveries"))
	assert.Equal(t, 400*time.Hour, period("llm_usage"))
}

// The tables are independent, so a session prune failing is no reason to let
// leases grow. The first error is still reported.
func TestRunOnce_OneFailureDoesNotStopTheOthers(t *testing.T) {
	sentinel := errors.New("session prune exploded")
	store := &fakeStore{errs: map[string]error{"sessions": sentinel}, deleted: 2}
	svc := prune.NewService(store, fullConfig(), slog.DiscardHandler)

	result, err := svc.RunOnce(context.Background())
	require.ErrorIs(t, err, sentinel)

	assert.Len(t, store.rules("sessions"), 1, "the failing table was attempted")
	assert.Len(t, store.rules("leases"), 1, "leases must still be pruned")
	assert.Equal(t, int64(2), result.Leases, "and their count reported")
}

func TestRunOnce_ZeroRetentionSkipsThatTableEntirely(t *testing.T) {
	store := &fakeStore{}
	svc := prune.NewService(store, prune.Config{SessionRetention: time.Hour}, slog.DiscardHandler)

	_, err := svc.RunOnce(context.Background())
	require.NoError(t, err)

	assert.Len(t, store.rules("sessions"), 1)
	assert.Empty(t, store.rules("leases"), "a zero retention must not reach the store at all")
	assert.Empty(t, store.rules("github_deliveries"))
	assert.Empty(t, store.rules("llm_usage"))
}

// jobRig runs the service's job on a fake clock, so a test steps through
// passes without sleeping.
type jobRig struct {
	clock  *clock.Fake
	sched  *jobs.Scheduler
	passes chan error
}

func startJob(t *testing.T, svc *prune.Service) *jobRig {
	t.Helper()
	r := &jobRig{clock: clock.NewFake(time.Unix(1_000_000, 0)), passes: make(chan error, 16)}
	r.sched = jobs.New(&jobs.Options{Clock: r.clock, AfterRun: func(_ string, err error) { r.passes <- err }})
	job := svc.Job()
	require.NotNil(t, job)
	_, err := r.sched.Start(context.Background(), job)
	require.NoError(t, err)
	t.Cleanup(r.sched.Close)
	return r
}

func (r *jobRig) pass(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.passes:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("no pass finished")
		return nil
	}
}

// A server restarted more often than the interval would otherwise never
// prune at all.
func TestJob_PrunesImmediatelyRatherThanWaitingOutTheFirstInterval(t *testing.T) {
	store := &fakeStore{}
	svc := prune.NewService(store, prune.Config{SessionRetention: time.Hour, Interval: time.Hour}, slog.DiscardHandler)

	r := startJob(t, svc)
	require.NoError(t, r.pass(t))
	assert.Len(t, store.rules("sessions"), 1, "the startup pass should not wait for the interval")
}

func TestJob_KeepsGoingAfterAFailure(t *testing.T) {
	store := &fakeStore{errs: map[string]error{"sessions": errors.New("transient")}}
	svc := prune.NewService(store, prune.Config{SessionRetention: time.Hour, Interval: time.Minute}, slog.DiscardHandler)

	// A database blip must not silently stop pruning forever -- that is
	// the failure that ends with a full disk.
	r := startJob(t, svc)
	require.Error(t, r.pass(t))
	r.clock.BlockUntil(1)
	r.clock.Advance(time.Minute)
	require.Error(t, r.pass(t))
	assert.Len(t, store.rules("sessions"), 2)
}

func TestJob_ZeroIntervalDisablesPruningEntirely(t *testing.T) {
	svc := prune.NewService(&fakeStore{}, prune.Config{SessionRetention: time.Hour}, slog.DiscardHandler)
	assert.Nil(t, svc.Job())
}

func TestRunOnce_ExpiresPendingActionsWhateverTheRetentionAndPrunesDecidedOnesByIt(t *testing.T) {
	store := &fakeStore{}
	svc := prune.NewService(store, prune.Config{PendingActionRetention: 500 * time.Hour}, slog.DiscardHandler)

	before := time.Now()
	_, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, store.expireCalls)
	assert.WithinDuration(t, before, store.expireNow, time.Minute, "expiry is measured against now, not a retention")
	pending := store.rules("pending_actions")
	require.Len(t, pending, 1)
	assert.Equal(t, 500*time.Hour, pending[0].Period)

	store = &fakeStore{}
	svc = prune.NewService(store, prune.Config{}, slog.DiscardHandler)
	_, err = svc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, store.expireCalls, "a zero retention keeps the decided ones but still closes the expired")
	assert.Empty(t, store.rules("pending_actions"))
}

func TestRunOnce_PrunesEndedConversationsByTheirRetentionAndNotAtAllWhenItIsZero(t *testing.T) {
	store := &fakeStore{deleted: 2}
	svc := prune.NewService(store, prune.Config{ConversationRetention: 100 * time.Hour}, slog.DiscardHandler)

	result, err := svc.RunOnce(context.Background())

	require.NoError(t, err)
	conversations := store.rules("conductor_conversations")
	require.Len(t, conversations, 1)
	assert.Equal(t, 100*time.Hour, conversations[0].Period)
	assert.Equal(t, int64(2), result.Conversations)
	assert.False(t, result.Empty())

	store = &fakeStore{}
	_, err = prune.NewService(store, prune.Config{}, slog.DiscardHandler).RunOnce(context.Background())
	require.NoError(t, err)
	assert.Empty(t, store.rules("conductor_conversations"))
}
