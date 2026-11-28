package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/uptrace/bun"

	clock "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
)

type cursorRow struct {
	bun.BaseModel `bun:"table:cursors"`

	System    string `bun:"system,pk"`
	Position  string `bun:"position"`
	UpdatedAt int64  `bun:"updated_at"`
}

// Cursors holds each TicketingSystem's upstream position, keyed by the
// system's name, so no ticket update is missed across downtime. A position
// is opaque here: its format is the system's.
type Cursors struct {
	db    *sqlitex.DB
	clock clock.Clock
}

// Get returns system's position; ok is false if none is recorded yet.
func (c *Cursors) Get(ctx context.Context, system string) (position string, ok bool, err error) {
	var row cursorRow
	err = c.db.NewSelect().Model(&row).Where("system = ?", system).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("state: reading cursor of %s: %w", system, err)
	}
	return row.Position, true, nil
}

// Set records system's position.
func (c *Cursors) Set(ctx context.Context, system, position string) error {
	row := &cursorRow{System: system, Position: position, UpdatedAt: toMillis(c.clock.Now())}
	_, err := c.db.NewInsert().Model(row).
		On("CONFLICT (system) DO UPDATE").Set("position = EXCLUDED.position").Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("state: setting cursor of %s: %w", system, err)
	}
	return nil
}

type settingRow struct {
	bun.BaseModel `bun:"table:settings"`

	Key       string `bun:"key,pk"`
	Value     string `bun:"value"`
	UpdatedAt int64  `bun:"updated_at"`
}

// The settings keys. Each has a typed accessor below.
const keyAutonomy = "autonomy.enabled"

// Settings holds what the daemon must persist that the user changes at run
// time, such as the autonomy toggle. An unset setting falls back to the
// config file.
type Settings struct {
	db    *sqlitex.DB
	clock clock.Clock
}

// Autonomy returns the persisted autonomy toggle; ok is false when it was
// never set, and the config's default applies.
func (s *Settings) Autonomy(ctx context.Context) (enabled, ok bool, err error) {
	v, ok, err := s.get(ctx, keyAutonomy)
	if err != nil || !ok {
		return false, ok, err
	}
	enabled, err = strconv.ParseBool(v)
	if err != nil {
		return false, false, fmt.Errorf("state: setting %s is %q: %w", keyAutonomy, v, err)
	}
	return enabled, true, nil
}

// SetAutonomy persists the autonomy toggle.
func (s *Settings) SetAutonomy(ctx context.Context, enabled bool) error {
	return s.set(ctx, keyAutonomy, strconv.FormatBool(enabled))
}

func (s *Settings) get(ctx context.Context, key string) (string, bool, error) {
	var row settingRow
	err := s.db.NewSelect().Model(&row).Where("key = ?", key).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("state: reading setting %s: %w", key, err)
	}
	return row.Value, true, nil
}

func (s *Settings) set(ctx context.Context, key, value string) error {
	row := &settingRow{Key: key, Value: value, UpdatedAt: toMillis(s.clock.Now())}
	_, err := s.db.NewInsert().Model(row).
		On("CONFLICT (key) DO UPDATE").Set("value = EXCLUDED.value").Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("state: setting %s: %w", key, err)
	}
	return nil
}
