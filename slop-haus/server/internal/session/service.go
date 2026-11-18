package session

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

type Store interface {
	Start(ctx context.Context, agentID uuid.UUID, claudeSessionID, cwd string) (*StartResult, error)
	Current(ctx context.Context, agentID uuid.UUID) (*Session, error)
	List(ctx context.Context) ([]Session, error)
	Touch(ctx context.Context, agentID uuid.UUID) error
	RecordProgress(ctx context.Context, agentID uuid.UUID) error
	TouchSession(ctx context.Context, agentID uuid.UUID, claudeSessionID string) (State, error)
	RecordBlock(ctx context.Context, agentID uuid.UUID, claudeSessionID string) (int, error)
	ResetBlocks(ctx context.Context, agentID uuid.UUID, claudeSessionID string) error
	Describe(ctx context.Context, agentID uuid.UUID, claudeSessionID string, d *Description) error
	MarkStuck(ctx context.Context, agentID uuid.UUID, claudeSessionID, reason string) error
}

type Service struct {
	store  Store
	logger *slog.Logger
}

func NewService(store Store, logHandler slog.Handler) *Service {
	return &Service{store: store, logger: slog.New(logHandler)}
}

func (s *Service) Start(ctx context.Context, agentID uuid.UUID, claudeSessionID, cwd string) (*StartResult, error) {
	result, err := s.store.Start(ctx, agentID, claudeSessionID, cwd)
	if err != nil {
		return nil, err
	}
	if result.ConcurrentRun {
		s.logger.Warn("two claude runs appear live for one agent",
			"agent_id", agentID, "claude_session_id", claudeSessionID)
	}
	return result, nil
}

func (s *Service) Current(ctx context.Context, agentID uuid.UUID) (*Session, error) {
	return s.store.Current(ctx, agentID)
}

func (s *Service) List(ctx context.Context) ([]Session, error) {
	return s.store.List(ctx)
}

func (s *Service) Touch(ctx context.Context, agentID uuid.UUID) error {
	return s.store.Touch(ctx, agentID)
}

// TouchSession records liveness for one Claude run and reports whether it
// is still the agent's current session. A false means the caller has been
// superseded.
func (s *Service) TouchSession(ctx context.Context, agentID uuid.UUID, claudeSessionID string) (State, error) {
	return s.store.TouchSession(ctx, agentID, claudeSessionID)
}

func (s *Service) RecordProgress(ctx context.Context, agentID uuid.UUID) error {
	return s.store.RecordProgress(ctx, agentID)
}

// RecordBlock counts a stop-block and reports whether the loop breaker has
// tripped.
func (s *Service) RecordBlock(ctx context.Context, agentID uuid.UUID, claudeSessionID string) (count int, tripped bool, err error) {
	count, err = s.store.RecordBlock(ctx, agentID, claudeSessionID)
	if err != nil {
		return 0, false, err
	}
	return count, count >= MaxStopBlocks, nil
}

// ResetBlocks starts a run's stop-block count again. See PostgresStore.ResetBlocks.
func (s *Service) ResetBlocks(ctx context.Context, agentID uuid.UUID, claudeSessionID string) error {
	return s.store.ResetBlocks(ctx, agentID, claudeSessionID)
}

// MarkStuck records why the loop breaker let the run stop, for the Agents
// screen. It stays until the agent next makes progress.
func (s *Service) MarkStuck(ctx context.Context, agentID uuid.UUID, claudeSessionID, reason string) error {
	return s.store.MarkStuck(ctx, agentID, claudeSessionID, reason)
}

// Describe records a live run's self-description, sanitised first so the
// store only ever holds what is fit to show. See Description for why it is
// never a trust boundary.
func (s *Service) Describe(ctx context.Context, agentID uuid.UUID, claudeSessionID string, d *Description) error {
	if d == nil {
		return nil
	}
	return s.store.Describe(ctx, agentID, claudeSessionID, d.Sanitized())
}
