package usersettings

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

// DefaultHistoryLimit and MaxHistoryLimit bound a page of a setting's history.
const (
	DefaultHistoryLimit = 50
	MaxHistoryLimit     = 200
)

// TenantService is Service for the tenant-scoped settings: one user's values
// within the context's tenant.
type TenantService struct {
	store    TenantStore
	registry *Registry
	logger   *slog.Logger
}

func NewTenantService(store TenantStore, registry *Registry, logHandler slog.Handler) *TenantService {
	return &TenantService{store: store, registry: registry.ForScope(ScopeTenant), logger: slog.New(logHandler)}
}

// List returns every tenant-scoped setting for the user, defaulted where unset
// in the current membership.
func (s *TenantService) List(ctx context.Context, userID uuid.UUID) ([]Setting, error) {
	stored, err := s.store.Latest(ctx, userID, s.registry.Names())
	if err != nil {
		return nil, err
	}
	return resolve(s.registry, s.logger, stored), nil
}

// Get returns one setting, or ErrUnknownSetting for a name this scope doesn't hold.
func (s *TenantService) Get(ctx context.Context, userID uuid.UUID, name string) (*Setting, error) {
	if _, ok := s.registry.Lookup(name); !ok {
		return nil, unknown(name)
	}
	stored, err := s.store.Latest(ctx, userID, []string{name})
	if err != nil {
		return nil, err
	}
	// resolve returns every registered name; pick this one.
	for _, st := range resolve(s.registry, s.logger, stored) {
		if st.Name == name {
			return &st, nil
		}
	}
	return nil, unknown(name)
}

func (s *TenantService) Set(ctx context.Context, userID uuid.UUID, name string, v Value) (*Setting, error) {
	if err := s.registry.Check(name, v); err != nil {
		return nil, err
	}
	st, err := s.store.Append(ctx, userID, name, v)
	if err != nil {
		return nil, err
	}
	return &Setting{Name: name, Value: st.Value, UpdatedAt: &st.CreatedAt}, nil
}

// History is one page of the user's changes to a setting, newest first. next
// is the cursor for the following page, 0 when this was the last.
func (s *TenantService) History(ctx context.Context, userID uuid.UUID, name string, before int64, limit int) (changes []Change, next int64, err error) {
	if _, ok := s.registry.Lookup(name); !ok {
		return nil, 0, unknown(name)
	}
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	limit = min(limit, MaxHistoryLimit)
	// One extra row says whether another page follows.
	rows, err := s.store.History(ctx, userID, name, before, limit+1)
	if err != nil {
		return nil, 0, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].ID
	}
	return rows, next, nil
}
