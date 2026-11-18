// Package inventorytest is the suite every app runs against its own data
// inventory, so the CI checks are one implementation, not a copy per app.
package inventorytest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/engine"

	"github.com/lowaa/slop-factory/go-privacy/inventory"
)

// MigrationVersionTable is the table the migration runner keeps its own
// state in. It holds no data about anyone.
const MigrationVersionTable = "goose_db_version"

// Config is what an app gives the suite.
type Config struct {
	// Inventory is the app's inventory file.
	Inventory []byte
	// Tables lists the tables of the app's migrated database. An app using
	// go-db's engine passes TablesFrom(querier).
	Tables func(ctx context.Context) ([]string, error)
	// ConfigSource is the path of the app's config source file, where its
	// env var names are declared.
	ConfigSource string
	// EnforcedRetention is the period, by retention class id, that the
	// app's code enforces by default (its config defaults). Each must equal
	// the inventory's period for the class. Optional: an app with nothing it
	// deletes itself leaves it nil.
	EnforcedRetention map[string]time.Duration
}

// Result is the outcome of one check.
type Result struct {
	Name string
	Err  error
}

// Run checks the inventory validates, covers every table in the migrated
// database, and accounts for every service the config can call, each as a
// subtest.
//
// The runtime check of cookies and storage keys, that every one set during
// the app's main flows is in the inventory's storage registry, is the app's
// e2e test's to make: it reads the same file (inventory.Load) and compares
// it to what the browser holds. That wiring belongs to each app's
// own integration work.
func Run(t *testing.T, cfg *Config) {
	t.Helper()
	for _, r := range Check(t.Context(), cfg) {
		t.Run(r.Name, func(t *testing.T) {
			require.NoError(t, r.Err)
		})
	}
}

// Check runs the checks and returns each one's result. Run reports them as
// subtests; Check is separate so the suite itself can be tested.
func Check(ctx context.Context, cfg *Config) []Result {
	inv, err := inventory.Load(cfg.Inventory)
	if err != nil {
		return []Result{{Name: "the inventory validates", Err: err}}
	}
	results := []Result{
		{Name: "the inventory validates"},
		{Name: "every table is in the inventory", Err: checkTables(ctx, cfg, inv)},
		{Name: "every service the config can call is in the inventory", Err: checkConfig(cfg, inv)},
		{Name: "fragments generate", Err: checkFragments(inv)},
	}
	if cfg.EnforcedRetention != nil {
		results = append(results, Result{
			Name: "the retention periods the code enforces are the inventory's",
			Err:  inv.CheckEnforcedPeriods(cfg.EnforcedRetention),
		})
	}
	return results
}

func checkTables(ctx context.Context, cfg *Config, inv *inventory.Inventory) error {
	tables, err := cfg.Tables(ctx)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return errors.New("the database has no tables, so nothing was checked")
	}
	return inv.CheckTables(tables, MigrationVersionTable)
}

func checkConfig(cfg *Config, inv *inventory.Inventory) error {
	src, err := os.ReadFile(cfg.ConfigSource)
	if err != nil {
		return err
	}
	keys, err := inventory.ConfigKeysFromSource(src)
	if err != nil {
		return err
	}
	return inv.CheckConfigKeys(keys)
}

func checkFragments(inv *inventory.Inventory) error {
	frags := inventory.NewFragments(inv)
	for _, name := range []string{
		inventory.FragmentCookieTable, inventory.FragmentSubProcessors,
		inventory.FragmentRetention, inventory.FragmentDataFlow,
	} {
		if text, ok := frags.Fragment(name); !ok || text == "" {
			return fmt.Errorf("fragment %s is empty", name)
		}
	}
	return nil
}

// TablesFrom returns a Config.Tables that reads the public schema's tables
// through q.
func TablesFrom(q engine.Querier) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) { return listTables(ctx, q) }
}

func listTables(ctx context.Context, q engine.Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`)
	if err != nil {
		return nil, fmt.Errorf("inventorytest: list tables: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("inventorytest: scan table: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
