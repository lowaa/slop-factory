package dbtestutil

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartWithRetry(t *testing.T) {
	boom := errors.New("shim timeout")
	// Each attempt fails differently, so a test can tell which error came back.
	failure := func(call int) error { return fmt.Errorf("%w: attempt %d", boom, call) }

	// fails fails n times (each returning a partial value), then succeeds.
	run := func(n, attempts int) (got string, err error, calls int, discarded []string) {
		got, err = startWithRetry(context.Background(), attempts, time.Millisecond,
			func(context.Context) (string, error) {
				calls++
				if calls <= n {
					return "partial", failure(calls)
				}
				return "ok", nil
			},
			func(v string) { discarded = append(discarded, v) })
		return
	}

	t.Run("succeeds after transient failures", func(t *testing.T) {
		got, err, calls, discarded := run(2, 3)
		require.NoError(t, err)
		assert.Equal(t, "ok", got)
		assert.Equal(t, 3, calls)
		assert.Equal(t, []string{"partial", "partial"}, discarded)
	})

	t.Run("exhausts attempts and returns the last error", func(t *testing.T) {
		_, err, calls, discarded := run(5, 3)
		require.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "attempt 3")
		assert.Equal(t, 3, calls)
		assert.Len(t, discarded, 3)
	})

	t.Run("stops when the context is cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		_, err := startWithRetry(ctx, 3, time.Hour,
			func(context.Context) (string, error) { calls++; cancel(); return "", boom },
			func(string) {})
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, calls)
	})
}
