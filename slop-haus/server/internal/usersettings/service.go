package usersettings

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Setting is a setting as a user sees it: the value that applies now.
type Setting struct {
	Name  string
	Value Value
	// UpdatedAt is nil while the user has never set it and the default applies.
	UpdatedAt *time.Time
}

// Service owns the rules about which settings exist and what they hold; the
// store only persists.
type Service struct {
	store    Store
	registry *Registry
	logger   *slog.Logger
}

func NewService(store Store, registry *Registry, logHandler slog.Handler) *Service {
	return &Service{store: store, registry: registry.ForScope(ScopeGlobal), logger: slog.New(logHandler)}
}

// List returns every known setting for the user, defaulted where unset.
func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Setting, error) {
	names := s.registry.Names()
	stored, err := s.store.Latest(ctx, userID, names)
	if err != nil {
		return nil, err
	}
	return resolve(s.registry, s.logger, stored), nil
}

// resolve lays the stored rows over the registry's defaults, in name order. A
// stored row whose name is no longer known is ignored, and so is one whose
// type no longer matches the registry (a setting retyped after it was written).
func resolve(registry *Registry, logger *slog.Logger, stored []Stored) []Setting {
	byName := make(map[string]Stored, len(stored))
	for _, st := range stored {
		byName[st.Name] = st
	}
	names := registry.Names()
	out := make([]Setting, 0, len(names))
	for _, name := range names {
		def, _ := registry.Lookup(name)
		setting := Setting{Name: name, Value: def.Default}
		if st, ok := byName[name]; ok {
			if st.Value.Type == def.Default.Type {
				at := st.CreatedAt
				setting.Value, setting.UpdatedAt = st.Value, &at
			} else {
				logger.Warn("ignoring a stored user setting of the wrong type", "setting", name, "stored", st.Value.Type, "want", def.Default.Type)
			}
		}
		out = append(out, setting)
	}
	return out
}

// Set appends a row for the user's setting.
func (s *Service) Set(ctx context.Context, userID uuid.UUID, name string, v Value) (*Setting, error) {
	if err := s.registry.Check(name, v); err != nil {
		return nil, err
	}
	st, err := s.store.Append(ctx, userID, name, v)
	if err != nil {
		return nil, err
	}
	return &Setting{Name: name, Value: st.Value, UpdatedAt: &st.CreatedAt}, nil
}
