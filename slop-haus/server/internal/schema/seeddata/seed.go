// Package seeddata puts rows into every table of a slop-haus database, as the
// app role across several tenants, and checks that a migration kept them.
// The migration tests run it over each migration in turn; cmd/upgrade_data
// runs it either side of the kube upgrade test's upgrade.
package seeddata

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// A seeder knows how to put one table's rows into a database at whatever
// version it is at. Every table the migrations create must have one: the
// seeded-migration walk fails on a table without, so a new table cannot
// silently skip being populated before the migrations that follow it run.
// Registry is the seeders by table name.
type Registry map[string]seeder

type seeder struct {
	// global seeders run once per round, as the app role with no tenant set;
	// the rest run once per tenant, in a transaction scoped to it.
	global bool
	// derivedFrom names the table whose seeder fills this one -- through a
	// trigger or a default -- so the walk checks it came out populated instead
	// of inserting rows the schema would not.
	derivedFrom string
	// after names tables to seed first that no foreign key orders this one
	// after, because a trigger rather than a constraint reads their rows.
	after []string
	run   func(s *seed) error
}

func tenantSeeder(run func(s *seed) error) seeder { return seeder{run: run} }
func tenantSeederAfter(run func(s *seed) error, tables ...string) seeder {
	return seeder{run: run, after: tables}
}
func globalSeeder(run func(s *seed) error) seeder { return seeder{global: true, run: run} }
func derivedSeeder(from string) seeder            { return seeder{derivedFrom: from} }

// row is an inserted row as the database holds it (to_jsonb of the row),
// kept so the seeders of dependent tables can point at it.
type row map[string]any

// world is what the seeders have written so far, across rounds, by tenant
// (the zero uuid for global seeders' own rows) and table.
type world struct {
	tenants []uuid.UUID
	made    map[uuid.UUID]map[string][]row
	// raw are the rows' JSON as the database returned them, by table: what a
	// snapshot reads them back as.
	raw map[string][]string
}

func newWorld(tenants []uuid.UUID) *world {
	return &world{tenants: tenants, made: map[uuid.UUID]map[string][]row{}, raw: map[string][]string{}}
}

func (w *world) record(tenant uuid.UUID, table string, r row, raw string) {
	w.raw[table] = append(w.raw[table], raw)
	if w.made[tenant] == nil {
		w.made[tenant] = map[string][]row{}
	}
	w.made[tenant][table] = append(w.made[tenant][table], r)
}

// seed is what a table's seeder works with: one round, one scope (a tenant,
// or global), against the schema as it is at version.
type seed struct {
	ctx     context.Context
	q       db.Querier
	w       *world
	version int64
	round   int
	epoch   time.Time
	tenant  uuid.UUID
	cols    map[string]map[string]bool
}

// DefaultEpoch is where seeded timestamps start unless the seeding says
// otherwise.
var DefaultEpoch = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// tenantIndex is stable across rounds, so a tag names the same tenant's row
// in every round.
func (s *seed) tenantIndex(tenant uuid.UUID) int {
	for i, t := range s.w.tenants {
		if t == tenant {
			return i
		}
	}
	return -1
}

// tag is a string unique to this round and tenant: unique columns take it
// so that seeding the next round into the same tables cannot collide.
func (s *seed) tag(label string) string {
	if s.tenant == uuid.Nil {
		return fmt.Sprintf("%s-r%d", label, s.round)
	}
	return fmt.Sprintf("%s-r%d-t%d", label, s.round, s.tenantIndex(s.tenant))
}

func (s *seed) id(label string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(s.tag(label)))
}

// at is a timestamp that differs by round, tenant and offset, so a backfill
// that copies the wrong timestamp column or row is visible.
func (s *seed) at(offset time.Duration) time.Time {
	return s.epoch.Add(time.Duration(s.round*7+max(s.tenantIndex(s.tenant), 0))*24*time.Hour + offset)
}

// as is the seed for another tenant, for a global seeder writing that
// tenant's rows: ids, tags and times come out per tenant and the rows are
// filed under it.
func (s *seed) as(tenant uuid.UUID) *seed {
	c := *s
	c.tenant = tenant
	return &c
}

// last is the most recent row seeded into table for this scope's tenant, or
// for another tenant when given.
func (s *seed) last(table string) row { return s.lastFor(s.tenant, table) }

func (s *seed) lastFor(tenant uuid.UUID, table string) row {
	rows := s.recentFor(tenant, table, 1)
	return rows[0]
}

// recent is the last n rows seeded into table, oldest first.
func (s *seed) recent(table string, n int) []row { return s.recentFor(s.tenant, table, n) }

func (s *seed) recentFor(tenant uuid.UUID, table string, n int) []row {
	rows := s.w.made[tenant][table]
	if len(rows) < n {
		panic(seedError{fmt.Errorf("seeder dependency: %d %s rows wanted for tenant %s, %d seeded (is the table's seeder ordered after its parents by foreign key?)", n, table, tenant, len(rows))})
	}
	return rows[len(rows)-n:]
}

func (r row) get(column string) any {
	v, ok := r[column]
	if !ok {
		panic(seedError{fmt.Errorf("seeded row has no column %q: %v", column, r)})
	}
	if n, ok := v.(json.Number); ok {
		i, err := n.Int64()
		if err != nil {
			panic(seedError{err})
		}
		return i
	}
	return v
}

// insert writes one row. Seeders describe the row against the latest schema,
// and a column the table does not have at this version (not added yet, or
// dropped since) is left out, so one description serves every version.
func (s *seed) insert(table string, values map[string]any) row {
	return s.insertAs(s.tenant, table, values, "")
}

// upsert is insert for the tables with one row per tenant: the next round
// replaces what the last wrote.
func (s *seed) upsert(table, conflictTarget string, values map[string]any) row {
	return s.insertAs(s.tenant, table, values, conflictTarget)
}

func (s *seed) insertAs(filedUnder uuid.UUID, table string, values map[string]any, conflictTarget string) row {
	have, ok := s.cols[table]
	if !ok {
		panic(seedError{fmt.Errorf("seed %s: the table does not exist at version %d", table, s.version)})
	}
	var names []string
	for name := range values {
		if have[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	args := make([]any, len(names))
	holders := make([]string, len(names))
	for i, name := range names {
		args[i] = values[name]
		holders[i] = fmt.Sprintf("$%d", i+1)
	}
	query := fmt.Sprintf("INSERT INTO %s AS t (%s) VALUES (%s)", table, strings.Join(names, ", "), strings.Join(holders, ", "))
	if conflictTarget != "" {
		sets := make([]string, len(names))
		for i, name := range names {
			sets[i] = fmt.Sprintf("%s = EXCLUDED.%s", name, name)
		}
		query += fmt.Sprintf(" ON CONFLICT %s DO UPDATE SET %s", conflictTarget, strings.Join(sets, ", "))
	}
	query += " RETURNING to_jsonb(t)"
	var raw []byte
	if err := s.q.QueryRow(s.ctx, query, args...).Scan(&raw); err != nil {
		panic(seedError{fmt.Errorf("insert into %s at version %d: %w", table, s.version, err)})
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var r row
	if err := dec.Decode(&r); err != nil {
		panic(seedError{err})
	}
	s.w.record(filedUnder, table, r, string(raw))
	return r
}

// seedError carries a database failure out of a seeder, which reads
// straight through its inserts instead of checking each one.
type seedError struct{ err error }

func (e seedError) Error() string { return e.err.Error() }
func (e seedError) Unwrap() error { return e.err }

// fingerprint is the table's content as one value, for noticing that a round
// changed it.
func (s *seed) fingerprint(table string) string {
	var f string
	if err := s.q.QueryRow(s.ctx,
		"SELECT COALESCE(md5(string_agg(to_jsonb(t)::text, ',' ORDER BY to_jsonb(t)::text)), '') FROM "+table+" t").Scan(&f); err != nil {
		panic(seedError{err})
	}
	return f
}

// requireChanged fails unless the round changed the table: rows added, or a
// counter moved. A derived table is filled by a trigger or default off its
// source's inserts; "has rows" would pass on the last round's.
func (s *seed) requireChanged(table, before string) {
	if s.fingerprint(table) == before {
		panic(seedError{fmt.Errorf("%s did not change in the round: its derivation (a trigger or a default) did not fire", table)})
	}
}

// seeding runs the seeders of every table present at the rig's current
// version, parents before children.
type Seeding struct {
	app       *db.DB
	seeders   Registry
	w         *world
	exercised map[string]bool
	// SkipUnseeded makes a table without a seeder skipped, and named in
	// Skipped, instead of an error. For a database this checkout did not
	// create: the one an upgrade starts from may be another revision's, with a
	// table this one has not heard of. The migration tests keep it off, which
	// is the guard.
	SkipUnseeded bool
	Skipped      []string
	// Epoch is where the rows' timestamps start. In a live environment it is
	// ahead of the clock: the app prunes what has expired or aged out and
	// sweeps what has lapsed, and a seeded row it takes for dead is a row the
	// test then reports lost.
	Epoch time.Time
}

// NewSeeding seeds through app, the role the server serves as, for the
// tenants named.
func NewSeeding(app *db.DB, seeders Registry, tenants []uuid.UUID) *Seeding {
	return &Seeding{app: app, seeders: seeders, w: newWorld(tenants), exercised: map[string]bool{}, Epoch: DefaultEpoch}
}

// MissingSeeders are the tables, among those the database has, that nothing
// seeds.
func MissingSeeders(tables []string, seeders Registry) []string {
	var missing []string
	for _, table := range tables {
		if _, ok := seeders[table]; !ok {
			missing = append(missing, table)
		}
	}
	return missing
}

// Unused are the seeders of tables no version of the schema ever
// had: a seeder outliving its table's rename or typo.
func (sd *Seeding) Unused() []string {
	var unused []string
	for table := range sd.seeders {
		if !sd.exercised[table] {
			unused = append(unused, table)
		}
	}
	sort.Strings(unused)
	return unused
}

func (sd *Seeding) columns(ctx context.Context) (map[string]map[string]bool, error) {
	rows, err := sd.app.Global().Query(ctx, `
		SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = 'public'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]map[string]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, err
		}
		if cols[table] == nil {
			cols[table] = map[string]bool{}
		}
		cols[table][column] = true
	}
	return cols, rows.Err()
}

// foreignKeyOrder sorts tables so each follows the tables it references.
func (sd *Seeding) foreignKeyOrder(ctx context.Context, tables []string) ([]string, error) {
	rows, err := sd.app.Global().Query(ctx, `
		SELECT c.conrelid::regclass::text, c.confrelid::regclass::text FROM pg_constraint c
		 WHERE c.contype = 'f' AND c.connamespace = 'public'::regnamespace`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	parents := map[string]map[string]bool{}
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			return nil, err
		}
		if child == parent {
			continue
		}
		if parents[child] == nil {
			parents[child] = map[string]bool{}
		}
		parents[child][parent] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for table, sdr := range sd.seeders {
		for _, parent := range sdr.after {
			if parents[table] == nil {
				parents[table] = map[string]bool{}
			}
			parents[table][parent] = true
		}
	}

	sorted := append([]string(nil), tables...)
	sort.Strings(sorted)
	var ordered []string
	done := map[string]bool{}
	for len(ordered) < len(sorted) {
		progressed := false
		for _, table := range sorted {
			if done[table] {
				continue
			}
			ready := true
			for parent := range parents[table] {
				if !done[parent] {
					ready = false
					break
				}
			}
			if ready {
				done[table] = true
				ordered = append(ordered, table)
				progressed = true
			}
		}
		if !progressed {
			return nil, fmt.Errorf("tables reference each other in a cycle; seeding order is undefined: %v", sorted)
		}
	}
	return derivedLast(ordered, sd.seeders), nil
}

// derivedLast moves the derived tables, which a trigger fills from their
// source's inserts -- something no foreign key says -- after everything else,
// so they are only checked once all the inserts ran.
func derivedLast(ordered []string, seeders Registry) []string {
	var direct, derived []string
	for _, table := range ordered {
		if seeders[table].derivedFrom != "" {
			derived = append(derived, table)
		} else {
			direct = append(direct, table)
		}
	}
	return append(direct, derived...)
}

// Round seeds every table at the given schema version. A table without a
// seeder is an error: the migrations after it would run over no rows in it,
// which is how a backfill once went untested.
func (sd *Seeding) Round(ctx context.Context, version int64, round int) (err error) {
	tables, err := Tables(ctx, sd.app.Global())
	if err != nil {
		return err
	}
	if missing := MissingSeeders(tables, sd.seeders); len(missing) > 0 && sd.SkipUnseeded {
		sd.Skipped = append(sd.Skipped, missing...)
		tables = withoutTables(tables, missing)
	} else if len(missing) > 0 {
		return fmt.Errorf("no seeder for %s: add one to seeddata.Seeders, so the migrations after the table's creation run over rows in it", strings.Join(missing, ", "))
	}
	for _, table := range tables {
		if isGlobalTable(table) {
			continue
		}
		hasTenantID, err := columnExists(ctx, sd.app.Global(), table, "tenant_id")
		if err != nil {
			return err
		}
		if !hasTenantID {
			return fmt.Errorf("%s is not a global_ table, so it must be scoped by a tenant_id column", table)
		}
	}
	cols, err := sd.columns(ctx)
	if err != nil {
		return err
	}
	order, err := sd.foreignKeyOrder(ctx, tables)
	if err != nil {
		return err
	}
	defer func() {
		if r := recover(); r != nil {
			se, ok := r.(seedError)
			if !ok {
				panic(r)
			}
			err = se
		}
	}()
	// Before anything is inserted, so a derived table's check is of this round.
	before := map[string]map[uuid.UUID]string{}
	for _, table := range order {
		if sd.seeders[table].derivedFrom == "" {
			continue
		}
		before[table] = map[uuid.UUID]string{}
		for _, tenant := range sd.w.tenants {
			err := sd.app.InTx(db.WithTenant(ctx, tenant), func(q db.Querier) error {
				before[table][tenant] = (&seed{ctx: ctx, q: q}).fingerprint(table)
				return nil
			})
			if err != nil {
				return fmt.Errorf("fingerprint %s: %w", table, err)
			}
		}
	}
	for _, table := range order {
		sd.exercised[table] = true
		sdr := sd.seeders[table]
		newSeed := func(q db.Querier, tenant uuid.UUID) *seed {
			return &seed{ctx: ctx, q: q, w: sd.w, version: version, round: round, epoch: sd.Epoch, tenant: tenant, cols: cols}
		}
		switch {
		case sdr.global:
			if err := sd.app.InGlobalTx(ctx, func(q db.Querier) error { return sdr.run(newSeed(q, uuid.Nil)) }); err != nil {
				return fmt.Errorf("seed %s: %w", table, err)
			}
		default:
			for _, tenant := range sd.w.tenants {
				tctx := db.WithTenant(ctx, tenant)
				err := sd.app.InTx(tctx, func(q db.Querier) error {
					s := newSeed(q, tenant)
					if sdr.derivedFrom != "" {
						s.requireChanged(table, before[table][tenant])
						return nil
					}
					return sdr.run(s)
				})
				if err != nil {
					return fmt.Errorf("seed %s for tenant %s: %w", table, tenant, err)
				}
			}
		}
	}
	return nil
}

func isGlobalTable(table string) bool { return strings.HasPrefix(table, "global_") }

func columnExists(ctx context.Context, q db.Querier, table, column string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns
		                WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2)`, table, column).Scan(&ok)
	return ok, err
}

// Tables are the base tables in the public schema, other than goose's own
// bookkeeping.
func Tables(ctx context.Context, q db.Querier) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname <> 'goose_db_version'
		 ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// SchemaVersion is the migration version the database is at.
func SchemaVersion(ctx context.Context, q db.Querier) (int64, error) {
	var v int64
	err := q.QueryRow(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&v)
	return v, err
}

func withoutTables(tables, drop []string) []string {
	var kept []string
	for _, t := range tables {
		if !slices.Contains(drop, t) {
			kept = append(kept, t)
		}
	}
	return kept
}

// OnlySeededGlobalRows narrows a snapshot's global tables to the rows this
// seeding wrote, to be found again later rather than matched exactly: in a
// live environment the app writes to those tables too, and a seeded row is
// the only one nobody else touches.
func (sd *Seeding) OnlySeededGlobalRows(snap *Snapshot) {
	snap.Subset = map[string]bool{}
	for table, byTenant := range snap.Rows {
		if !isGlobalTable(table) {
			continue
		}
		rows := append([]string(nil), sd.w.raw[table]...)
		sort.Strings(rows)
		byTenant[uuid.Nil] = rows
		snap.Subset[table] = true
	}
}
