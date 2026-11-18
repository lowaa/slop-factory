package landing

import (
	"context"
	"fmt"
)

// OrderedQueue lists the tenant's landing rows in landing order; *PostgresQueue is one.
type OrderedQueue interface {
	Ordered(ctx context.Context) ([]LandingRow, error)
}

// ChangeTools says whether the tenant has a code host credential, which the
// server needs to merge for it; githubapp.Service is one.
type ChangeTools interface {
	Connected(ctx context.Context) (bool, error)
}

// Capabilities is what the tenant can choose for merge_method, and what it has.
type Capabilities struct {
	MergeMethod string
	// MergeQueueEnabled is the server-wide MERGE_QUEUE_ENABLED kill switch.
	MergeQueueEnabled bool
	// ChangeTools is whether the tenant has a code host credential.
	ChangeTools bool
}

// MergeQueueAvailable is whether merge_queue can be chosen at all.
func (c *Capabilities) MergeQueueAvailable() bool { return c.MergeQueueEnabled && c.ChangeTools }

// Status reads the landing queue and the tenant's capabilities for the board.
type Status struct {
	queue    OrderedQueue
	settings SettingReader
	tools    ChangeTools
	enabled  bool
}

// NewStatus takes enabled from MERGE_QUEUE_ENABLED. tools may be nil, when no
// code host can be connected.
func NewStatus(queue OrderedQueue, s SettingReader, tools ChangeTools, enabled bool) *Status {
	return &Status{queue: queue, settings: s, tools: tools, enabled: enabled}
}

func (s *Status) Capabilities(ctx context.Context) (*Capabilities, error) {
	set, err := s.settings.Get(ctx, SettingMergeMethod)
	if err != nil {
		return nil, fmt.Errorf("landing: merge method: %w", err)
	}
	c := &Capabilities{MergeMethod: set.Value.Str, MergeQueueEnabled: s.enabled}
	if s.tools != nil {
		if c.ChangeTools, err = s.tools.Connected(ctx); err != nil {
			return nil, fmt.Errorf("landing: code host credential: %w", err)
		}
	}
	return c, nil
}

// Queue is every row, in landing order, then those handed back.
func (s *Status) Queue(ctx context.Context) ([]LandingRow, error) {
	return s.queue.Ordered(ctx)
}

var _ OrderedQueue = (*PostgresQueue)(nil)
