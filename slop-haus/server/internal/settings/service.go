package settings

import (
	"context"
	"errors"
	"fmt"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Service owns the rules: which settings exist, what kind each holds, and
// what a missing row means. Store owns the SQL.
type Service struct {
	store  Store
	events *audit.Recorder
	logger *slog.Logger
	owners Owners
	gate   *authn.OwnerGate
	// onProvisionEnabled runs when provision_from_board turns on.
	onProvisionEnabled ProvisionEnabledHook
}

// ProvisionEnabledHook is what turning provision_from_board on must also do.
// It is an interface, injected from main, so settings does not import the
// agent package that reads this one.
type ProvisionEnabledHook interface {
	ImportAll(ctx context.Context) (int, error)
}

// Owners says who may change an owner-only setting.
type Owners interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// Option configures a Service.
type Option func(*Service)

// WithOwners lets tenant owners change the owner-only settings. Without it
// those settings cannot be changed at all.
func WithOwners(o Owners) Option { return func(s *Service) { s.owners = o } }

// WithProvisionEnabledHook makes switching provision_from_board on also run
// the hook; if the hook fails the switch is undone and the write fails.
func WithProvisionEnabledHook(h ProvisionEnabledHook) Option {
	return func(s *Service) { s.onProvisionEnabled = h }
}

func NewService(store Store, events *audit.Recorder, logHandler slog.Handler, opts ...Option) *Service {
	s := &Service{store: store, events: events, logger: slog.New(logHandler)}
	for _, o := range opts {
		o(s)
	}
	s.gate = authn.NewOwnerGate(s.owners, authn.WithRefusal(ErrNotOwner))
	return s
}

// Get returns the stored value, or the declared default when nothing has
// been written yet.
func (s *Service) Get(ctx context.Context, name string) (Setting, error) {
	def, err := Default(name)
	if err != nil {
		return Setting{}, err
	}

	stored, err := s.store.Get(ctx, name)
	switch {
	case errors.Is(err, ErrNotStored):
		return Setting{Name: name, Value: def}, nil
	case err != nil:
		return Setting{}, err
	}

	// A stored row whose type no longer matches the declaration is a
	// setting that changed shape between releases. Reporting the default
	// is the safe answer and the mismatch is logged rather than swallowed:
	// silently coercing it would hand callers a value the declaration says
	// is impossible.
	if stored.Value.Type != def.Type {
		s.logger.Warn("stored setting has the wrong type; using the default",
			"setting", name, "stored", stored.Value.Type, "declared", def.Type)
		return Setting{Name: name, Value: def}, nil
	}
	return *stored, nil
}

// List returns every known setting, stored or defaulted, in a stable
// order. The UI renders this, so a setting that has never been written
// still appears -- with its default -- rather than being missing until
// somebody happens to change it.
func (s *Service) List(ctx context.Context) ([]Setting, error) {
	stored, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}

	names := Names()
	out := make([]Setting, 0, len(names))
	for _, name := range names {
		def, err := Default(name)
		if err != nil {
			return nil, err
		}
		if found, ok := stored[name]; ok && found.Value.Type == def.Type {
			out = append(out, found)
			continue
		}
		out = append(out, Setting{Name: name, Value: def})
	}
	return out, nil
}

// GetMany returns the named settings from one read, each stored or defaulted
// as Get would report it. An unknown name is ErrUnknownSetting.
func (s *Service) GetMany(ctx context.Context, names ...string) (map[string]Setting, error) {
	for _, name := range names {
		if _, err := Default(name); err != nil {
			return nil, err
		}
	}
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]Setting, len(all))
	for _, st := range all {
		byName[st.Name] = st
	}
	out := make(map[string]Setting, len(names))
	for _, name := range names {
		out[name] = byName[name]
	}
	return out, nil
}

// Set writes a value as the user by, refusing an unknown setting and one
// whose type does not match the declaration. A write that changes nothing is
// not recorded.
//
// Both refusals matter for the same reason: this table is read by code
// that asks for a name and a type, so a row that is either unrecognised or
// the wrong shape is one nothing will ever read correctly. Catching it at
// the write is the only point where the caller is still around to be told.
//
// Every change is on record in the audit log with who, the setting and the
// values before and after: the ticket-processing switch decides whether the
// whole fleet works.
func (s *Service) Set(ctx context.Context, name string, value Value, by uuid.UUID) (Setting, error) {
	if err := known.reg().Check(name, value); err != nil {
		return Setting{}, err
	}
	if err := validate(name, value); err != nil {
		return Setting{}, err
	}
	if err := s.requireOwner(ctx, name, by); err != nil {
		return Setting{}, err
	}
	written, err := s.store.Upsert(ctx, name, value, by)
	if err != nil {
		return Setting{}, err
	}
	if written.Changed && name == ProvisionFromBoard && value.Bool && s.onProvisionEnabled != nil {
		// The flag is written first so a slot reported while this runs is
		// seen by the next sync; it is undone on failure so the flag is
		// never left on with slots stranded. The undo ignores a cancelled request
		// for the same reason.
		if _, err := s.onProvisionEnabled.ImportAll(ctx); err != nil {
			if _, rbErr := s.store.Upsert(context.WithoutCancel(ctx), name, written.Previous, by); rbErr != nil {
				err = errors.Join(err, fmt.Errorf("undo %s: %w", name, rbErr))
			}
			return Setting{}, fmt.Errorf("import reported slots: %w", err)
		}
	}
	if written.Changed {
		tenant, _ := db.TenantFrom(ctx)
		oldValue, newValue := fmt.Sprint(display(written.Previous)), fmt.Sprint(display(written.Setting.Value))
		s.events.Record(ctx, audit.SettingChanged,
			"user_id", by, "tenant", tenant, "setting", name, "old", oldValue, "new", newValue)
		s.logger.Info("setting changed", "setting", name, "type", value.Type, "old", oldValue, "new", newValue)
	}
	return written.Setting, nil
}

// DefaultHistoryLimit and MaxHistoryLimit bound a page of a setting's history.
const (
	DefaultHistoryLimit = 50
	MaxHistoryLimit     = 200
)

// Change is one write to a setting: what it read before and after, when, and
// who did it.
type Change struct {
	ID   int64
	From Value
	To   Value
	At   time.Time
	// By is nil when no person is on record; see Revision.
	By        *Actor
	ByDeleted bool
}

// BeforeHistoryKept reports a change with no person on record that is not
// explained by a removed account: it predates history being kept. It must not
// be shown as a deleted user.
func (c Change) BeforeHistoryKept() bool { return c.By == nil && !c.ByDeleted }

// History is one page of a setting's changes, newest first. next is the
// cursor for the following page, 0 when this was the last.
//
// A change's From is the value stored just before it, or the declared default
// for the first. One extra row is read: it says whether another page follows
// and is the From of the page's last change.
func (s *Service) History(ctx context.Context, name string, before int64, limit int) (changes []Change, next int64, err error) {
	def, err := Default(name)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	limit = min(limit, MaxHistoryLimit)

	rows, err := s.store.History(ctx, name, before, limit+1)
	if err != nil {
		return nil, 0, err
	}
	changes = make([]Change, 0, min(len(rows), limit))
	for i, r := range rows[:min(len(rows), limit)] {
		from := def
		if i+1 < len(rows) {
			from = rows[i+1].Value
		}
		changes = append(changes, Change{ID: r.ID, From: from, To: r.Value, At: r.At, By: r.ChangedBy, ByDeleted: r.ChangedByDeleted})
	}
	if len(rows) > limit {
		next = rows[limit-1].ID
	}
	return changes, next, nil
}

// display is the one field of a value that carries it.
func display(v Value) any {
	switch v.Type {
	case TypeStr:
		return v.Str
	case TypeInt:
		return v.Int
	case TypeBool:
		return v.Bool
	case TypeFloat:
		return v.Float
	default:
		return nil
	}
}

// TicketProcessingEnabled reports whether the fleet may start new work.
//
// This is the typed accessor the claim path and the Stop hook call, rather
// than either of them reaching for Get and unpacking a Value. An error is
// returned rather than defaulted: the callers are deciding whether to hand
// out work, and guessing "yes" because the database was unreachable is
// exactly the wrong way to be wrong.
func (s *Service) TicketProcessingEnabled(ctx context.Context) (bool, error) {
	found, err := s.Get(ctx, TicketProcessingEnabled)
	if err != nil {
		return false, err
	}
	return found.Value.Bool, nil
}

// ProvisionFromBoardEnabled reports whether the tenant provisions its
// supervisors' agents from the board.
func (s *Service) ProvisionFromBoardEnabled(ctx context.Context) (bool, error) {
	found, err := s.Get(ctx, ProvisionFromBoard)
	if err != nil {
		return false, err
	}
	return found.Value.Bool, nil
}

// WorkspaceReconcileEnabled reports whether supervisors reconcile their slots'
// checkouts against the workspace manifest.
func (s *Service) WorkspaceReconcileEnabled(ctx context.Context) (bool, error) {
	found, err := s.Get(ctx, WorkspaceReconcile)
	if err != nil {
		return false, err
	}
	return found.Value.Bool, nil
}

// requireOwner refuses a non-owner's write to an owner-only setting, and any
// such write when no owner check is configured: failing open would hand an
// ordinary member the steward's switches.
func (s *Service) requireOwner(ctx context.Context, name string, by uuid.UUID) error {
	if !ownerOnly(name) {
		return nil
	}
	return s.gate.RequireOwnerInContext(ctx, by)
}
