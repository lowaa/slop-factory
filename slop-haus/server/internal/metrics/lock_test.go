package metrics_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
)

type fakeLocks struct {
	locks []metrics.LockLoad
	err   error
}

func (f *fakeLocks) LockLoads(context.Context) ([]metrics.LockLoad, error) {
	return f.locks, f.err
}

type oneTenant struct{}

func (oneTenant) Tenants(context.Context) ([]uuid.UUID, error) { return []uuid.UUID{uuid.New()}, nil }

func gather(t *testing.T, src metrics.LockSource) (map[string]float64, error) {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(metrics.NewLockCollector(src, oneTenant{}, slog.NewTextHandler(io.Discard, nil))))
	mfs, err := reg.Gather()
	out := map[string]float64{}
	for _, mf := range mfs {
		out[mf.GetName()] = mf.GetMetric()[0].GetGauge().GetValue()
	}
	return out, err
}

func TestLockCollector_HeldLockAndItsQueue(t *testing.T) {
	got, err := gather(t, &fakeLocks{locks: []metrics.LockLoad{
		{Held: 4 * time.Hour, Waiters: 3},
		{Held: time.Minute, Waiters: 1},
	}})
	require.NoError(t, err)
	assert.Equal(t, 4*time.Hour.Seconds(), got["slophaus_integration_lock_held_seconds"], "the longest hold")
	assert.EqualValues(t, 4, got["slophaus_integration_lock_waiters"])
}

func TestLockCollector_FreeLockIsZero(t *testing.T) {
	got, err := gather(t, &fakeLocks{locks: []metrics.LockLoad{{}}})
	require.NoError(t, err)
	assert.Contains(t, got, "slophaus_integration_lock_held_seconds")
	assert.Zero(t, got["slophaus_integration_lock_held_seconds"])
	assert.Zero(t, got["slophaus_integration_lock_waiters"])
}

func TestLockCollector_AFailedReadIsAGapNotAZero(t *testing.T) {
	got, err := gather(t, &fakeLocks{err: errors.New("db down")})
	require.Error(t, err)
	assert.NotContains(t, got, "slophaus_integration_lock_held_seconds")
}
