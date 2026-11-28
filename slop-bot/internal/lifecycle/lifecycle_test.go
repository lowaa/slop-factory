package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestFuncWithoutHooks(t *testing.T) {
	t.Parallel()
	f := &Func{N: "noop"}
	assert.NoError(t, f.Start(context.Background()))
	assert.NoError(t, f.Stop(context.Background()))
}
