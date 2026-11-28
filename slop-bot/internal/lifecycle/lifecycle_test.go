package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
)

type recorder struct{ calls []string }

func (r *recorder) comp(name string, startErr, stopErr error) Component {
	return &Func{
		N:         name,
		StartFunc: func(context.Context) error { r.calls = append(r.calls, "start "+name); return startErr },
		StopFunc:  func(context.Context) error { r.calls = append(r.calls, "stop "+name); return stopErr },
	}
}

var quiet = slog.New(slog.DiscardHandler)

func TestStartsInOrderStopsInReverse(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	g := NewGroup(quiet, r.comp("db", nil, nil), r.comp("socket", nil, nil), r.comp("scheduler", nil, nil))
	require.NoError(t, g.Start(context.Background()))
	require.NoError(t, g.Stop(context.Background()))
	assert.Equal(t, []string{
		"start db", "start socket", "start scheduler",
		"stop scheduler", "stop socket", "stop db",
	}, r.calls)
}

func TestAFailedStartStopsWhatAlreadyStarted(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	boom := errors.New("boom")
	g := NewGroup(quiet, r.comp("db", nil, nil), r.comp("socket", boom, nil), r.comp("scheduler", nil, nil))
	err := g.Start(context.Background())
	require.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "starting socket")
	assert.Equal(t, []string{"start db", "start socket", "stop db"}, r.calls)
}

func TestStopCarriesOnPastAFailure(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	e1, e2 := errors.New("e1"), errors.New("e2")
	g := NewGroup(quiet, r.comp("a", nil, e1), r.comp("b", nil, nil), r.comp("c", nil, e2))
	require.NoError(t, g.Start(context.Background()))
	err := g.Stop(context.Background())
	assert.ErrorIs(t, err, e1)
	assert.ErrorIs(t, err, e2)
	assert.Equal(t, []string{"start a", "start b", "start c", "stop c", "stop b", "stop a"}, r.calls)
}

func TestStopTwiceStopsOnce(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	g := NewGroup(quiet, r.comp("a", nil, nil))
	require.NoError(t, g.Start(context.Background()))
	require.NoError(t, g.Stop(context.Background()))
	require.NoError(t, g.Stop(context.Background()))
	assert.Equal(t, []string{"start a", "stop a"}, r.calls)
}

// exiter is a component whose Stop gives up at once, while it runs on
// until release closes.
type exiter struct {
	release, exited chan struct{}
}

func (e *exiter) Name() string                { return "sweep" }
func (e *exiter) Start(context.Context) error { return nil }
func (e *exiter) Exited() <-chan struct{}     { return e.exited }
func (e *exiter) Stop(context.Context) error  { return errors.New("gave up at its deadline") }

// warned closes logged at the first warning logged.
type warned struct {
	slog.Handler
	once   sync.Once
	logged chan struct{}
}

func (h *warned) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }

func (h *warned) Handle(context.Context, slog.Record) error {
	h.once.Do(func() { close(h.logged) })
	return nil
}

// A component still running after its Stop gave up holds back stopping
// what it depends on until it has exited: a sweep still in a database
// mustn't see it closed.
func TestStopWaitsForAComponentThatOutlivedItsStop(t *testing.T) {
	t.Parallel()
	sweep := &exiter{release: make(chan struct{}), exited: make(chan struct{})}
	go func() {
		<-sweep.release
		close(sweep.exited)
	}()
	storeStopped := make(chan bool, 1)
	store := &Func{N: "store", StopFunc: func(context.Context) error {
		select {
		case <-sweep.exited:
			storeStopped <- true
		default:
			storeStopped <- false
		}
		return nil
	}}
	h := &warned{Handler: slog.DiscardHandler, logged: make(chan struct{})}
	g := NewGroup(slog.New(h), store, sweep)
	require.NoError(t, g.Start(context.Background()))
	stopped := make(chan error, 1)
	go func() { stopped <- g.Stop(context.Background()) }()
	// The group warns that it is waiting; stopping the store instead fails.
	select {
	case <-h.logged:
	case exited := <-storeStopped:
		require.Fail(t, "the store was stopped without waiting for the sweep", "sweep exited: %v", exited)
	}
	close(sweep.release)
	assert.True(t, <-storeStopped, "the store stopped after the sweep exited")
	assert.ErrorContains(t, <-stopped, "gave up at its deadline")
}

// One that had exited by the time its Stop failed holds nothing up.
func TestStopDoesntWaitForAComponentThatHasExited(t *testing.T) {
	t.Parallel()
	sweep := &exiter{exited: make(chan struct{})}
	close(sweep.exited)
	r := &recorder{}
	h := &warned{Handler: slog.DiscardHandler, logged: make(chan struct{})}
	g := NewGroup(slog.New(h), r.comp("store", nil, nil), sweep)
	require.NoError(t, g.Start(context.Background()))
	require.ErrorContains(t, g.Stop(context.Background()), "gave up at its deadline")
	assert.Equal(t, []string{"start store", "stop store"}, r.calls)
	select {
	case <-h.logged:
		assert.Fail(t, "warned of a wait that didn't happen")
	default:
	}
}

func TestFuncWithoutHooks(t *testing.T) {
	t.Parallel()
	f := &Func{N: "noop"}
	assert.NoError(t, f.Start(context.Background()))
	assert.NoError(t, f.Stop(context.Background()))
}

// The wait for components that outlived their Stop is bounded, all of them
// together, ExitWait past the first: the service manager kills the daemon
// at a deadline derived from it, and a kill mid-close is worse than closing
// what a component still uses. What is given up on is said.
func TestTheWaitForComponentsThatOutlivedTheirStopIsBounded(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 11, 29, 9, 0, 0, 0, time.UTC))
	never := &exiter{exited: make(chan struct{})} // never exits
	alsoNever := &exiter{exited: make(chan struct{})}
	storeStopped := make(chan struct{})
	store := &Func{N: "store", StopFunc: func(context.Context) error { close(storeStopped); return nil }}
	h := &warned{Handler: slog.DiscardHandler, logged: make(chan struct{})}
	g := NewGroup(slog.New(h), store, never, alsoNever).BoundExits(time.Minute, clk)
	require.NoError(t, g.Start(context.Background()))
	stopped := make(chan error, 1)
	go func() { stopped <- g.Stop(context.Background()) }()
	<-h.logged // waiting, with its deadline set
	clk.Advance(time.Minute)
	<-storeStopped
	err := <-stopped
	assert.ErrorContains(t, err, "gave up at its deadline")
	assert.ErrorContains(t, err, "still running")
}
