package dbtestutil

import (
	"context"
	"fmt"
	"time"
)

const (
	startAttempts = 3
	// A containerd shim timeout on a loaded CI host clears in seconds, so a
	// short fixed pause is enough; a long one only delays a real failure.
	startBackoff = 3 * time.Second
)

// startWithRetry calls start up to attempts times, pausing backoff between
// tries, and returns the first success or the last error. Only the container
// start goes through it: a setup or migration failure is deterministic and
// must fail at once. start may return a partly created value alongside its
// error (testcontainers does); discard releases it before the next attempt.
func startWithRetry[T any](ctx context.Context, attempts int, backoff time.Duration,
	start func(context.Context) (T, error), discard func(T)) (T, error) {
	var (
		val T
		err error
	)
	for i := 1; i <= attempts; i++ {
		val, err = start(ctx)
		if err == nil {
			return val, nil
		}
		discard(val)
		if i == attempts {
			break
		}
		select {
		case <-ctx.Done():
			return val, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return val, fmt.Errorf("after %d attempts: %w", attempts, err)
}
