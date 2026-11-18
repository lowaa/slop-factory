package settings

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	gosettings "github.com/lowaa/slop-factory/go-settings"
	"github.com/lowaa/slop-factory/go-settings/pgstore"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Store is the persistence Service needs. Declared here rather than beside
// the Postgres implementation so the dependency points inward, matching
// ticket.Store.
type Store interface {
	// Get returns ErrNotStored when the setting has never been written,
	// which the service reads as "use the declared default".
	Get(ctx context.Context, name string) (*Setting, error)
	List(ctx context.Context) (map[string]Setting, error)
	// Upsert records value as made by the user by, unless it changes nothing.
	Upsert(ctx context.Context, name string, value Value, by uuid.UUID) (*Written, error)
	// History returns the setting's stored values, newest first, those with
	// an id below before (0 for the newest), at most limit.
	History(ctx context.Context, name string, before int64, limit int) ([]Revision, error)
}

// Revision is one stored value of a setting and who stored it.
type Revision struct {
	ID    int64
	Value Value
	At    time.Time
	// ChangedBy is nil when no person is on record, for either reason
	// ChangedByDeleted tells apart.
	ChangedBy *Actor
	// ChangedByDeleted says ChangedBy is nil because the person's account was
	// removed. False with a nil ChangedBy is a row from before history was
	// kept, or one no person made.
	ChangedByDeleted bool
}

// Actor is a person who changed a setting.
type Actor struct {
	ID   uuid.UUID
	Name string
	// FormerMember is true when they no longer belong to the tenant.
	FormerMember bool
}

// Written is what an Upsert did.
type Written struct {
	// Setting is the setting as it now reads.
	Setting Setting
	// Previous is what it read before: the declared default if it was never
	// written.
	Previous Value
	// Changed is false when the value was already what the setting read, in
	// which case nothing was recorded.
	Changed bool
}

// ErrNotStored means no row exists yet, not that the setting is unknown --
// the two are different answers and only Service can tell them apart,
// since the registry of known settings lives there.
var ErrNotStored = errors.New("settings: not stored")

// ErrNotOwner is a write to an owner-only setting by someone who is not one.
var ErrNotOwner = errors.New("settings: only a tenant owner can change this setting")

// ErrNoActor means a write named no user. Settings are changed by people; the
// history would otherwise record a change nobody made.
var ErrNoActor = errors.New("settings: no user to record the change against")

// PostgresStore reads and writes tenant_settings.
type PostgresStore struct {
	db    *db.DB
	table *pgstore.Table
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database, table: pgstore.TenantTable("tenant_settings")}
}

var _ Store = (*PostgresStore)(nil)

func toSetting(st gosettings.Stored) Setting {
	return Setting{Name: st.Name, Value: st.Value, UpdatedAt: st.CreatedAt}
}

func (s *PostgresStore) latest(ctx context.Context, q db.Querier, names []string) ([]gosettings.Stored, error) {
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	return s.table.Latest(ctx, q, tenant, names)
}

func (s *PostgresStore) Get(ctx context.Context, name string) (*Setting, error) {
	var rows []gosettings.Stored
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		var err error
		rows, err = s.latest(ctx, q, []string{name})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("get setting %q: %w", name, err)
	}
	if len(rows) == 0 {
		return nil, ErrNotStored
	}
	found := toSetting(rows[0])
	return &found, nil
}

func (s *PostgresStore) List(ctx context.Context) (map[string]Setting, error) {
	var rows []gosettings.Stored
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		var err error
		rows, err = s.latest(ctx, q, Names())
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	out := make(map[string]Setting, len(rows))
	for _, st := range rows {
		out[st.Name] = toSetting(st)
	}
	return out, nil
}

// Upsert appends the value to the tenant's history, if it differs from what
// the setting reads now.
//
// It runs inside a transaction that first takes the claim lock, which is
// what makes the ticket-processing switch mean something. The claim path
// reads that switch inside its own transaction under the same lock, so
// without this a write could commit between a claim's read and its grant
// -- handing an agent a ticket moments after the fleet was paused, which
// is the one thing the switch exists to prevent. Every setting takes the
// lock rather than just that one: settings writes are a human pressing a
// button, so the cost is nil, and a per-setting exception is a rule
// somebody eventually gets wrong.
//
// The lock also serialises writers, so the comparison with the latest row
// cannot race another write of the same setting.
func (s *PostgresStore) Upsert(ctx context.Context, name string, value Value, by uuid.UUID) (*Written, error) {
	if by == uuid.Nil {
		return nil, ErrNoActor
	}
	def, err := Default(name)
	if err != nil {
		return nil, err
	}
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}

	var written *Written
	err = s.db.InTx(ctx, func(q db.Querier) error {
		if err := db.TakeAdvisoryLock(ctx, q, db.ClaimLockKey); err != nil {
			return err
		}
		rows, err := s.table.Latest(ctx, q, tenant, []string{name})
		if err != nil {
			return err
		}
		current, previous := Setting{Name: name, Value: def}, def
		if len(rows) > 0 {
			current = toSetting(rows[0])
			previous = rows[0].Value
			// A stored value of another type is read as the default (see
			// Service.Get), so a write of the default is no change either, and
			// reports the default.
			if previous.Type != def.Type {
				current, previous = Setting{Name: name, Value: def}, def
			}
		}
		if previous.Same(value) {
			written = &Written{Setting: current, Previous: previous}
			return nil
		}

		st, err := s.table.AppendBy(ctx, q, tenant, name, value, by)
		if err != nil {
			return err
		}
		written = &Written{Setting: toSetting(*st), Previous: previous, Changed: true}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("upsert setting %q: %w", name, err)
	}
	return written, nil
}

func (s *PostgresStore) History(ctx context.Context, name string, before int64, limit int) ([]Revision, error) {
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	var changes []gosettings.Change
	err = s.db.InReadTx(ctx, func(q db.Querier) error {
		var err error
		changes, err = s.table.History(ctx, q, tenant, name, before, limit)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("history of setting %q: %w", name, err)
	}

	actors, err := s.actors(ctx, tenant, changes)
	if err != nil {
		return nil, fmt.Errorf("history of setting %q: %w", name, err)
	}
	out := make([]Revision, 0, len(changes))
	for _, c := range changes {
		r := Revision{ID: c.ID, Value: c.Value, At: c.CreatedAt, ChangedByDeleted: c.ChangedByDeleted}
		if c.ChangedBy != nil {
			if a, ok := actors[*c.ChangedBy]; ok {
				r.ChangedBy = &a
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// actors names the people behind changes. Only ids that this tenant's own
// history holds are looked up, so the global user table is never browsed.
func (s *PostgresStore) actors(ctx context.Context, tenant uuid.UUID, changes []gosettings.Change) (map[uuid.UUID]Actor, error) {
	var ids []uuid.UUID
	for _, c := range changes {
		if c.ChangedBy != nil {
			ids = append(ids, *c.ChangedBy)
		}
	}
	out := make(map[uuid.UUID]Actor, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Global().Query(ctx, `
		SELECT u.id, u.display_name,
		       NOT EXISTS (SELECT 1 FROM global_tenant_memberships m WHERE m.tenant = $2 AND m.user_id = u.id)
		  FROM global_users u
		 WHERE u.id = ANY($1)`, ids, tenant)
	if err != nil {
		return nil, fmt.Errorf("name who changed: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a Actor
		if err := rows.Scan(&a.ID, &a.Name, &a.FormerMember); err != nil {
			return nil, fmt.Errorf("name who changed: %w", err)
		}
		out[a.ID] = a
	}
	return out, rows.Err()
}

// EnabledIn reads a bool setting through q, falling back to its declared
// default when no row is stored.
//
// It takes a Querier rather than going through Service because its caller
// needs the answer inside its own transaction: the claim path reads the
// ticket-processing switch under the claim lock, which is what keeps a
// pause from landing between a claim's check and its grant (see Upsert).
// A Service call would run in a transaction of its own and reopen that gap.
//
// The newest row decides, and one of another type reads as the default, the
// same answer Service.Get gives for it.
func EnabledIn(ctx context.Context, q db.Querier, name string) (bool, error) {
	def, err := Default(name)
	if err != nil {
		return false, err
	}
	if def.Type != TypeBool {
		return false, fmt.Errorf("%w: %q is %s, not bool", ErrTypeMismatch, name, def.Type)
	}

	var enabled *bool
	err = q.QueryRow(ctx, `
		SELECT bool_value FROM tenant_settings
		 WHERE tenant_id = current_tenant() AND setting_name = $1
		 ORDER BY id DESC LIMIT 1`,
		name).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && enabled == nil) {
		return def.Bool, nil
	}
	if err != nil {
		return false, fmt.Errorf("read setting %q: %w", name, err)
	}
	return *enabled, nil
}

// IntIn is EnabledIn for an int setting.
func IntIn(ctx context.Context, q db.Querier, name string) (int64, error) {
	def, err := Default(name)
	if err != nil {
		return 0, err
	}
	if def.Type != TypeInt {
		return 0, fmt.Errorf("%w: %q is %s, not int", ErrTypeMismatch, name, def.Type)
	}

	var n *int64
	err = q.QueryRow(ctx, `
		SELECT int_value FROM tenant_settings
		 WHERE tenant_id = current_tenant() AND setting_name = $1
		 ORDER BY id DESC LIMIT 1`,
		name).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && n == nil) {
		return def.Int, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read setting %q: %w", name, err)
	}
	return *n, nil
}
