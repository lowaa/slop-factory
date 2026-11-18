package clock

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestFakeTickerFiresOnAdvance(t *testing.T) {
	f := NewFake(t0)
	tk := f.NewTicker(time.Second)
	defer tk.Stop()
	f.Advance(999 * time.Millisecond)
	select {
	case <-tk.C():
		t.Fatal("ticked early")
	default:
	}
	f.Advance(time.Millisecond)
	require.Equal(t, t0.Add(time.Second), <-tk.C())
	require.Equal(t, t0.Add(time.Second), f.Now())
	f.Advance(time.Second)
	require.Equal(t, t0.Add(2*time.Second), <-tk.C())
}

func TestFakeAfterZeroFiresAtOnce(t *testing.T) {
	f := NewFake(t0)
	require.Equal(t, t0, <-f.After(0))
}

func TestFakeAfterFiresOnce(t *testing.T) {
	f := NewFake(t0)
	ch := f.After(time.Minute)
	require.Equal(t, 1, f.Waiters())
	f.Advance(time.Minute)
	<-ch
	require.Equal(t, 0, f.Waiters())
}

func TestFakeStoppedTickerDoesNotFire(t *testing.T) {
	f := NewFake(t0)
	tk := f.NewTicker(time.Second)
	tk.Stop()
	f.Advance(time.Hour)
	select {
	case <-tk.C():
		t.Fatal("stopped ticker fired")
	default:
	}
}

func TestWithDeadlineCancelsWhenFakeAdvances(t *testing.T) {
	f := NewFake(t0)
	ctx, cancel := WithDeadline(context.Background(), f, t0.Add(time.Minute))
	defer cancel()
	f.BlockUntil(1)
	require.NoError(t, ctx.Err())
	f.Advance(time.Minute)
	<-ctx.Done()
}

func TestWithDeadlinePast(t *testing.T) {
	f := NewFake(t0)
	ctx, cancel := WithDeadline(context.Background(), f, t0.Add(-time.Second))
	defer cancel()
	require.Error(t, ctx.Err())
}

func TestRealTicks(t *testing.T) {
	tk := Real{}.NewTicker(time.Millisecond)
	defer tk.Stop()
	<-tk.C()
}

func TestFuncIsNow(t *testing.T) {
	var c Clock = Func(func() time.Time { return t0 })
	require.Equal(t, t0, c.Now())
}
