package agentmsg

import (
	"context"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Store is the persistence Service needs, declared here with its consumer.
type Store interface {
	// Insert fails with ErrAgentNotFound, ErrAgentDisabled or
	// ErrTooManyPending, all decided under one lock on the agent.
	Insert(ctx context.Context, agentID, authorUserID uuid.UUID, body string, maxPending int) (*Message, error)
	// Claim must mark and return in one statement: see PostgresStore.Claim.
	Claim(ctx context.Context, agentID uuid.UUID) ([]Message, error)
	CountPending(ctx context.Context, agentID uuid.UUID) (int, error)
	// Recent returns the newest messages first.
	Recent(ctx context.Context, limit int) ([]Message, error)
}

type Service struct {
	store  Store
	cfg    Config
	logger *slog.Logger
}

func NewService(store Store, cfg Config, logHandler slog.Handler) *Service {
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = DefaultMaxBody
	}
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = DefaultMaxPending
	}
	return &Service{store: store, cfg: cfg, logger: slog.New(logHandler)}
}

// Send holds a message from a person for an agent in the caller's tenant.
// The author is a person by type: there is no way to name an agent here.
func (s *Service) Send(ctx context.Context, who *authn.User, agentID uuid.UUID, body string) (*Message, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	if strings.TrimSpace(body) == "" {
		return nil, ErrEmptyBody
	}
	if utf8.RuneCountInString(body) > s.cfg.MaxBody {
		return nil, ErrTooLong
	}
	return s.store.Insert(ctx, agentID, who.ID, body, s.cfg.MaxPending)
}

// Deliver hands the agent everything pending for it, once: what it returns
// is marked delivered, so the caller must put it in front of the agent.
func (s *Service) Deliver(ctx context.Context, agentID uuid.UUID) ([]Message, error) {
	return s.store.Claim(ctx, agentID)
}

// Pending is how many messages wait for the agent, without delivering
// them: what an idle agent's wait and the Stop hook ask.
func (s *Service) Pending(ctx context.Context, agentID uuid.UUID) (int, error) {
	return s.store.CountPending(ctx, agentID)
}

// Recent is the newest messages first, for the Agents screen.
func (s *Service) Recent(ctx context.Context, who *authn.User, limit int) ([]Message, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	return s.store.Recent(ctx, min(max(limit, 1), 500))
}

// MaxBody is the message length limit, for the wire's schema.
func (s *Service) MaxBody() int { return s.cfg.MaxBody }
