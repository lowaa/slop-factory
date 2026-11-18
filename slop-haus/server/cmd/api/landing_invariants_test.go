package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
)

type invariantSinkFake struct{ last map[string]int }

func (s *invariantSinkFake) SetInvariantViolations(c map[string]int) { s.last = c }

func newInvariantMonitorFake(tenants []uuid.UUID, found func(uuid.UUID) ([]landing.Violation, error)) (*invariantMonitor, *invariantSinkFake) {
	sink := &invariantSinkFake{}
	return &invariantMonitor{
		tenants: func(context.Context) ([]uuid.UUID, error) { return tenants, nil },
		enabled: func(ctx context.Context) (bool, error) { return true, nil },
		check:   func(_ context.Context, t uuid.UUID) ([]landing.Violation, error) { return found(t) },
		sink:    sink, log: slog.New(slog.DiscardHandler), interval: time.Minute, seen: map[string]bool{},
	}, sink
}

func TestInvariantMonitor_CountsAViolationOnceASecondPassSeesIt(t *testing.T) {
	tenant := uuid.New()
	v := landing.Violation{Rule: landing.RulePositionsOrdered, BatchID: 4}
	m, sink := newInvariantMonitorFake([]uuid.UUID{tenant}, func(uuid.UUID) ([]landing.Violation, error) { return []landing.Violation{v}, nil })

	m.Pass(context.Background())
	assert.Empty(t, sink.last, "a first sighting may be a write between two reads")

	m.Pass(context.Background())
	assert.Equal(t, map[string]int{landing.RulePositionsOrdered: 1}, sink.last)
}

func TestInvariantMonitor_AClearedViolationIsForgotten(t *testing.T) {
	tenant := uuid.New()
	has := true
	m, sink := newInvariantMonitorFake([]uuid.UUID{tenant}, func(uuid.UUID) ([]landing.Violation, error) {
		if has {
			return []landing.Violation{{Rule: landing.RuleOneEnginePerApp, TicketID: 3}}, nil
		}
		return nil, nil
	})
	m.Pass(context.Background())
	m.Pass(context.Background())
	has = false
	m.Pass(context.Background())
	assert.Empty(t, sink.last)
	has = true
	m.Pass(context.Background())
	assert.Empty(t, sink.last, "it comes back as a first sighting")
}

func TestInvariantMonitor_SkipsATenantWhoseQueueIsOffAndSurvivesAnError(t *testing.T) {
	off, bad, good := uuid.New(), uuid.New(), uuid.New()
	m, sink := newInvariantMonitorFake([]uuid.UUID{off, bad, good}, func(tn uuid.UUID) ([]landing.Violation, error) {
		switch tn {
		case off:
			t.Errorf("the tenant with the queue off was checked")
		case bad:
			return nil, errors.New("db down")
		}
		return []landing.Violation{{Rule: landing.RuleBatchMembersAgree, TicketID: 1}}, nil
	})
	m.enabled = func(ctx context.Context) (bool, error) {
		tn, err := db.TenantFrom(ctx)
		return tn != off, err
	}
	m.Pass(context.Background())
	m.Pass(context.Background())
	assert.Equal(t, map[string]int{landing.RuleBatchMembersAgree: 1}, sink.last)
}

func TestInvariantMonitor_ATenantWhoseCheckFailsKeepsItsCounts(t *testing.T) {
	tenant := uuid.New()
	fail := false
	m, sink := newInvariantMonitorFake([]uuid.UUID{tenant}, func(uuid.UUID) ([]landing.Violation, error) {
		if fail {
			return nil, errors.New("db down")
		}
		return []landing.Violation{{Rule: landing.RuleOneEnginePerApp, TicketID: 3}}, nil
	})
	m.Pass(context.Background())
	m.Pass(context.Background())
	fail = true
	m.Pass(context.Background())
	assert.Equal(t, map[string]int{landing.RuleOneEnginePerApp: 1}, sink.last, "a blind check does not clear the alert")
	fail = false
	m.Pass(context.Background())
	assert.Equal(t, map[string]int{landing.RuleOneEnginePerApp: 1}, sink.last, "and the sighting survived it")
}
