package session_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
)

type fixture struct {
	svc      *session.Service
	database *db.DB
	ctx      context.Context
}

// noopReleaser stands in for the ticket service; these tests never disable
// an agent.
type noopReleaser struct{}

func (noopReleaser) ReleaseLeasesFor(context.Context, uuid.UUID, string, time.Time) error { return nil }

func newFixture(t *testing.T) (*fixture, uuid.UUID) {
	t.Helper()
	database := dbtest.WithDB(t)
	agents := agent.NewService(agent.NewPostgresStore(database), noopReleaser{}, nil, slog.DiscardHandler)

	// A real global_tenants row: agent.Service.Create writes a credential
	// referencing it by foreign key.
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	created, err := agents.Create(ctx, "builder-1")
	require.NoError(t, err)

	return &fixture{
		svc:      session.NewService(session.NewPostgresStore(database), slog.DiscardHandler),
		database: database,
		ctx:      ctx,
	}, created.ID
}

func TestStart_NewSession(t *testing.T) {
	f, agentID := newFixture(t)

	result, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	assert.Equal(t, session.ArrivalNew, result.Arrival)
	assert.True(t, result.Session.Current())
	assert.False(t, result.ConcurrentRun)
}

// A hook retry is the same session arriving twice and must change nothing.
// A close-then-insert would shut the session it had just created, open a
// third, and reset the loop-protection counters.
func TestStart_SameSessionTwiceIsIdempotent(t *testing.T) {
	f, agentID := newFixture(t)

	first, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)

	// Accumulate some state the retry must not clear.
	_, _, err = f.svc.RecordBlock(f.ctx, agentID, "claude-1")
	require.NoError(t, err)

	second, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	assert.Equal(t, session.ArrivalResumed, second.Arrival)
	assert.Equal(t, first.Session.ID, second.Session.ID, "the retry must adopt, not replace")
	assert.Equal(t, 1, second.Session.StopBlockCount, "counters must survive a retry")

	var count int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT count(*) FROM sessions WHERE agent_id = $1`, agentID).Scan(&count))
	assert.Equal(t, 1, count, "a retry must not open a third session")
}

// A retry that arrives after a newer session superseded it must not
// resurrect itself over the live one.
func TestStart_StaleRetryDoesNotResurrectItself(t *testing.T) {
	f, agentID := newFixture(t)

	_, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	newer, err := f.svc.Start(f.ctx, agentID, "claude-2", "/srv/ws/1")
	require.NoError(t, err)

	stale, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	assert.Equal(t, session.ArrivalStale, stale.Arrival)
	assert.Equal(t, newer.Session.ID, stale.Session.ID,
		"a stale retry is told what the current session is")

	current, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "claude-2", current.ClaudeSessionID)
}

func TestStart_NewSessionEndsThePrevious(t *testing.T) {
	f, agentID := newFixture(t)

	first, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	second, err := f.svc.Start(f.ctx, agentID, "claude-2", "/srv/ws/1")
	require.NoError(t, err)
	assert.Equal(t, session.ArrivalNew, second.Arrival)

	// At most one current session per agent -- the partial unique index
	// says so, and this proves the rebind respects it.
	var open int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT count(*) FROM sessions WHERE agent_id = $1 AND ended_at IS NULL`,
		agentID).Scan(&open))
	assert.Equal(t, 1, open)
	assert.NotEqual(t, first.Session.ID, second.Session.ID)
}

// Two live runs in one workspace is a human-level mistake the server
// detects rather than prevents -- fencing would mean putting a session id
// back into every agent request, and would not stop two processes fighting
// over the same working tree anyway.
func TestStart_DetectsASecondLiveRun(t *testing.T) {
	f, agentID := newFixture(t)

	_, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)

	second, err := f.svc.Start(f.ctx, agentID, "claude-2", "/srv/ws/1")
	require.NoError(t, err)
	assert.True(t, second.ConcurrentRun, "the replaced session was still live")
}

func TestStart_ConcurrentStartsLeaveOneCurrentSession(t *testing.T) {
	f, agentID := newFixture(t)

	const racers = 6
	done := make(chan error, racers)
	for i := range racers {
		go func(n int) {
			_, err := f.svc.Start(f.ctx, agentID,
				"claude-"+string(rune('a'+n)), "/srv/ws/1")
			done <- err
		}(i)
	}
	for range racers {
		require.NoError(t, <-done)
	}

	var open int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT count(*) FROM sessions WHERE agent_id = $1 AND ended_at IS NULL`,
		agentID).Scan(&open))
	assert.Equal(t, 1, open, "the agent row lock must serialize every binder")
}

// The loop breaker counts blocks against progress, not blocks in a row: an
// agent working steadily through a queue must never be declared stuck for
// doing exactly what it should.
func TestRecordBlock_ProgressResetsTheCounter(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)

	count, tripped, err := f.svc.RecordBlock(f.ctx, agentID, "claude-1")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.False(t, tripped)

	count, _, err = f.svc.RecordBlock(f.ctx, agentID, "claude-1")
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	require.NoError(t, f.svc.RecordProgress(f.ctx, agentID))

	count, tripped, err = f.svc.RecordBlock(f.ctx, agentID, "claude-1")
	require.NoError(t, err)
	assert.Equal(t, 1, count, "progress since the last block restarts the count")
	assert.False(t, tripped)
}

func TestRecordBlock_TripsAtTheLimit(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)

	var tripped bool
	for range session.MaxStopBlocks {
		_, tripped, err = f.svc.RecordBlock(f.ctx, agentID, "claude-1")
		require.NoError(t, err)
	}
	assert.True(t, tripped, "the loop breaker must trip at MaxStopBlocks")
}

func TestRecordBlock_WithoutASession(t *testing.T) {
	f, agentID := newFixture(t)

	_, _, err := f.svc.RecordBlock(f.ctx, agentID, "claude-1")
	require.ErrorIs(t, err, session.ErrNotFound)
}

// A superseded `claude` process keeps its terminal, its token and its Stop
// hook, so it goes on firing callbacks. Those must not touch the session
// that replaced it: repeated blocks from a dead run would otherwise trip
// the live run's loop breaker, letting a working agent stop because an old
// one kept calling.
func TestRecordBlock_FromASupersededSessionDoesNotTouchTheCurrentOne(t *testing.T) {
	f, agentID := newFixture(t)

	_, err := f.svc.Start(f.ctx, agentID, "claude-old", "/srv/ws/1")
	require.NoError(t, err)
	_, err = f.svc.Start(f.ctx, agentID, "claude-new", "/srv/ws/1")
	require.NoError(t, err)

	// The old run keeps calling.
	for range session.MaxStopBlocks + 2 {
		_, _, err := f.svc.RecordBlock(f.ctx, agentID, "claude-old")
		require.ErrorIs(t, err, session.ErrNotFound,
			"a superseded session has no row to count against")
	}

	current, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Zero(t, current.StopBlockCount,
		"the live session's loop breaker must be untouched by a dead run")
}

// The same for liveness: a dead process must not keep a live session
// looking healthy on the Agents screen.
func TestTouchSession_OnlyMatchesTheCurrentRun(t *testing.T) {
	f, agentID := newFixture(t)

	_, err := f.svc.Start(f.ctx, agentID, "claude-old", "/srv/ws/1")
	require.NoError(t, err)
	_, err = f.svc.Start(f.ctx, agentID, "claude-new", "/srv/ws/1")
	require.NoError(t, err)

	stale, err := f.svc.TouchSession(f.ctx, agentID, "claude-old")
	require.NoError(t, err)
	assert.Equal(t, session.StateSuperseded, stale,
		"a run replaced by a newer one is superseded, not merely 'not current'")

	live, err := f.svc.TouchSession(f.ctx, agentID, "claude-new")
	require.NoError(t, err)
	assert.Equal(t, session.StateLive, live)
}

// A session id nobody has seen, for an agent with no current session, is
// not superseded -- it is a live run whose SessionStart never landed.
//
// Conflating the two silently disables the loop: the Stop hook dismisses
// every callback, and because the loop breaker counts against a session
// row that does not exist, the agent can neither be kept working nor
// released. That is exactly what happened the first time real Claude Code
// connected with a session that predated the hook configuration.
func TestTouchSession_UnknownSessionIsNotSuperseded(t *testing.T) {
	f, agentID := newFixture(t)

	state, err := f.svc.TouchSession(f.ctx, agentID, "never-registered")
	require.NoError(t, err)
	assert.Equal(t, session.StateUnknown, state)
}

// An agent killed without a newer SessionStart leaves its row open
// indefinitely. Such a ghost must not dismiss a genuinely new run -- that
// would disable the loop permanently, and silently, because SessionStart
// never fires to rebind.
func TestTouchSession_AGhostCurrentSessionDoesNotSupersedeANewRun(t *testing.T) {
	f, agentID := newFixture(t)

	started, err := f.svc.Start(f.ctx, agentID, "claude-abandoned", "/srv/ws/1")
	require.NoError(t, err)

	// Not seen since well before the liveness window: the process is gone,
	// but nothing ever closed its row.
	_, err = f.database.Exec(f.ctx,
		`UPDATE sessions SET last_seen_at = now() - interval '1 hour' WHERE id = $1`,
		started.Session.ID)
	require.NoError(t, err)

	state, err := f.svc.TouchSession(f.ctx, agentID, "a-live-run-that-never-registered")
	require.NoError(t, err)
	assert.Equal(t, session.StateUnknown, state,
		"a stale session must not speak for the agent")
}

// A run that has already been replaced stays replaced. Without this, two
// live processes in one workspace would ping-pong -- each callback
// rebinding over the other, forever -- and neither would keep a coherent
// loop counter.
func TestTouchSession_AnAlreadyReplacedRunStaysSuperseded(t *testing.T) {
	f, agentID := newFixture(t)

	_, err := f.svc.Start(f.ctx, agentID, "claude-old", "/srv/ws/1")
	require.NoError(t, err)
	_, err = f.svc.Start(f.ctx, agentID, "claude-new", "/srv/ws/1")
	require.NoError(t, err)

	state, err := f.svc.TouchSession(f.ctx, agentID, "claude-old")
	require.NoError(t, err)
	assert.Equal(t, session.StateSuperseded, state)
}

// A new run takes over from whatever was current, however healthy that
// incumbent looks.
//
// This is the rule the whole loop depends on, because SessionStart does
// not fire in Claude Code: a Stop callback is the only evidence a session
// exists. Keeping the incumbent meant the first session an agent ever
// bound stayed current forever and every later run was dismissed -- which
// is not a test artifact, it is how it behaved in a real terminal three
// times running.
func TestTouchSession_ANewRunTakesOverFromALiveIncumbent(t *testing.T) {
	f, agentID := newFixture(t)

	_, err := f.svc.Start(f.ctx, agentID, "claude-incumbent", "/srv/ws/1")
	require.NoError(t, err)

	state, err := f.svc.TouchSession(f.ctx, agentID, "claude-brand-new")
	require.NoError(t, err)
	assert.Equal(t, session.StateUnknown, state,
		"a session never seen before is a new run, not a superseded one")
}

func TestDescribe_RecordsOnTheLiveRunAndReplacesWhatItSaidBefore(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "run-1", "/w")
	require.NoError(t, err)

	current, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, session.Description{Labels: map[string]string{}}, current.Description,
		"an undescribed run reads back empty, not nil labels")

	require.NoError(t, f.svc.Describe(f.ctx, agentID, "run-1", &session.Description{
		Harness: "pi", HarnessVersion: "0.87.0", Provider: "anthropic", Model: "claude-sonnet-4-5",
		Labels: map[string]string{"gpu": "a100"},
	}))
	current, err = f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, session.Description{
		Harness: "pi", HarnessVersion: "0.87.0", Provider: "anthropic", Model: "claude-sonnet-4-5",
		Labels: map[string]string{"gpu": "a100"},
	}, current.Description)

	require.NoError(t, f.svc.Describe(f.ctx, agentID, "run-1", &session.Description{
		Harness: "pi", Model: "claude-opus-5-5",
	}))

	listed, err := f.svc.List(f.ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, session.Description{
		Harness: "pi", HarnessVersion: "0.87.0", Model: "claude-opus-5-5", Labels: map[string]string{},
	}, listed[0].Description, "the version is kept: only the transcript-derived fields survive a hook that omits them")
}

// Claude Code's Stop carries no model; describe-session.py reads it from
// the transcript, and a lookup that finds none must not blank what an
// earlier hook recorded. The same for the config revision, fixed when the
// run started.
func TestDescribe_AHookThatFindsNoModelKeepsTheLastKnownOne(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "run-1", "/w")
	require.NoError(t, err)

	revision := int64(7)
	require.NoError(t, f.svc.Describe(f.ctx, agentID, "run-1", &session.Description{
		Harness: "claude", HarnessVersion: "2.1.283", Model: "claude-sonnet-4-5", ConfigRevision: &revision,
	}))
	require.NoError(t, f.svc.Describe(f.ctx, agentID, "run-1", &session.Description{Harness: "claude"}))

	current, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5", current.Description.Model)
	assert.Equal(t, "2.1.283", current.Description.HarnessVersion)
	require.NotNil(t, current.Description.ConfigRevision)
	assert.Equal(t, int64(7), *current.Description.ConfigRevision)

	// The revision is the one the run started with: a later hook cannot
	// move it.
	other := int64(9)
	require.NoError(t, f.svc.Describe(f.ctx, agentID, "run-1", &session.Description{
		Harness: "claude", ConfigRevision: &other,
	}))
	current, err = f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(7), *current.Description.ConfigRevision)

	// A model that did change still lands.
	require.NoError(t, f.svc.Describe(f.ctx, agentID, "run-1", &session.Description{
		Harness: "claude", Model: "claude-opus-5-5",
	}))
	current, err = f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5-5", current.Description.Model)
}

// A superseded process keeps its hooks; it must not relabel its
// replacement, and a run with no live row is simply not described.
func TestDescribe_OnlyTouchesTheNamedLiveRun(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "old", "/w")
	require.NoError(t, err)
	_, err = f.svc.Start(f.ctx, agentID, "new", "/w")
	require.NoError(t, err)

	err = f.svc.Describe(f.ctx, agentID, "old", &session.Description{Harness: "claude"})
	assert.ErrorIs(t, err, session.ErrNotFound)
	err = f.svc.Describe(f.ctx, agentID, "never-seen", &session.Description{Harness: "claude"})
	assert.ErrorIs(t, err, session.ErrNotFound)

	current, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "new", current.ClaudeSessionID)
	assert.Empty(t, current.Description.Harness)
}

// The service sanitises before the store sees anything, so a hostile
// description cannot trip the column checks and fail the write.
func TestDescribe_SanitisesBeforeStoring(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "run-1", "/w")
	require.NoError(t, err)

	require.NoError(t, f.svc.Describe(f.ctx, agentID, "run-1", &session.Description{
		Model: "\x1b[31m" + strings.Repeat("m", 500),
	}))
	current, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "[31m"+strings.Repeat("m", session.MaxDescriptionField-4), current.Description.Model)
}

// trip counts the blocks that trip the loop breaker.
func (f *fixture) trip(t *testing.T, agentID uuid.UUID, claudeSessionID string) {
	t.Helper()
	for range session.MaxStopBlocks {
		_, _, err := f.svc.RecordBlock(f.ctx, agentID, claudeSessionID)
		require.NoError(t, err)
	}
}

func TestMarkStuck_ShowsOnTheSessionUntilTheAgentNextMakesProgress(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	current, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Nil(t, current.Stuck, "a run starts clear")

	f.trip(t, agentID, "claude-1")
	require.NoError(t, f.svc.MarkStuck(f.ctx, agentID, "claude-1", "blocked 3 times"))
	current, err = f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, current.Stuck)
	assert.Equal(t, "blocked 3 times", current.Stuck.Reason)
	assert.WithinDuration(t, time.Now(), current.Stuck.At, time.Minute)

	require.NoError(t, f.svc.RecordProgress(f.ctx, agentID))
	current, err = f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Nil(t, current.Stuck, "progress is the agent's way out of it")
}

func TestMarkStuck_ANewRunStartsClear(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	f.trip(t, agentID, "claude-1")
	require.NoError(t, f.svc.MarkStuck(f.ctx, agentID, "claude-1", "stuck"))

	_, err = f.svc.Start(f.ctx, agentID, "claude-2", "/srv/ws/1")
	require.NoError(t, err)
	current, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Nil(t, current.Stuck)
}

// A superseded process keeps calling its hooks; it must not mark the
// replacement stuck, nor find a session to mark when it has none.
func TestMarkStuck_OnlyTouchesTheNamedLiveRun(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "claude-old", "/srv/ws/1")
	require.NoError(t, err)
	_, err = f.svc.Start(f.ctx, agentID, "claude-new", "/srv/ws/1")
	require.NoError(t, err)

	f.trip(t, agentID, "claude-new")
	require.ErrorIs(t, f.svc.MarkStuck(f.ctx, agentID, "claude-old", "stuck"), session.ErrNotFound)
	current, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Nil(t, current.Stuck)
}

// Stop counts the block and marks in two statements; progress landing
// between them must win, or a stuck notice would outlive the progress that
// cleared it.
func TestMarkStuck_IsNotWrittenOnceProgressHasResetTheBreaker(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	f.trip(t, agentID, "claude-1")
	require.NoError(t, f.svc.RecordProgress(f.ctx, agentID))

	require.ErrorIs(t, f.svc.MarkStuck(f.ctx, agentID, "claude-1", "stale"), session.ErrNotFound)
	current, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Nil(t, current.Stuck)
}

// A human nudging a run that the breaker let stop gives it a fresh budget
// without calling that progress: the stuck notice stays up until it does
// something.
func TestResetBlocks_RestartsTheCountAndLeavesTheStuckNoticeAlone(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	f.trip(t, agentID, "claude-1")
	require.NoError(t, f.svc.MarkStuck(f.ctx, agentID, "claude-1", "stuck"))
	before, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)

	require.NoError(t, f.svc.ResetBlocks(f.ctx, agentID, "claude-1"))

	after, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Zero(t, after.StopBlockCount)
	require.NotNil(t, after.Stuck, "only progress clears the banner")
	assert.Equal(t, before.Stuck.At, after.Stuck.At, "and the nudge does not restamp it")
	assert.Equal(t, before.LastProgressAt, after.LastProgressAt, "a nudge is not progress")

	count, tripped, err := f.svc.RecordBlock(f.ctx, agentID, "claude-1")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.False(t, tripped)
}

func TestResetBlocks_OnlyTouchesTheNamedLiveRun(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "claude-old", "/srv/ws/1")
	require.NoError(t, err)
	_, err = f.svc.Start(f.ctx, agentID, "claude-new", "/srv/ws/1")
	require.NoError(t, err)
	f.trip(t, agentID, "claude-new")

	require.NoError(t, f.svc.ResetBlocks(f.ctx, agentID, "claude-old"))

	current, err := f.svc.Current(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, session.MaxStopBlocks, current.StopBlockCount,
		"a dead run's prompt must not hand the live run more blocks")
}

// The bundle a workspace says it runs is the latest it said: an update between
// two hooks changes it, and a hook that cannot read the file must not blank it.
func TestDescribe_TheBundleHashFollowsTheLatestReportAndIsNotBlanked(t *testing.T) {
	f, agentID := newFixture(t)
	_, err := f.svc.Start(f.ctx, agentID, "run-1", "/w")
	require.NoError(t, err)
	bundle := func() string {
		current, err := f.svc.Current(f.ctx, agentID)
		require.NoError(t, err)
		return current.Description.BundleHash
	}

	require.NoError(t, f.svc.Describe(f.ctx, agentID, "run-1", &session.Description{Harness: "claude", BundleHash: "aaaa"}))
	assert.Equal(t, "aaaa", bundle())

	require.NoError(t, f.svc.Describe(f.ctx, agentID, "run-1", &session.Description{Harness: "claude"}))
	assert.Equal(t, "aaaa", bundle(), "a hook that reports none leaves it")

	require.NoError(t, f.svc.Describe(f.ctx, agentID, "run-1", &session.Description{Harness: "claude", BundleHash: "bbbb"}))
	assert.Equal(t, "bbbb", bundle(), "the workspace updated since")
}
