package seeddata

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Snapshot is every row of every table, as the app role sees it: a tenant's
// tables once per tenant, through the RLS policies that scope them, and the
// global_ tables once. Reading as the app role makes the snapshot a check
// that each tenant still reads its own rows after a migration, too.
type Snapshot struct {
	Columns map[string][]string `json:"columns"`
	// Rows[table][tenant] are the rows' JSON, sorted. Global tables are filed
	// under the zero uuid.
	Rows map[string]map[uuid.UUID][]string `json:"rows"`
	// Subset marks the tables whose rows are only to be found again after a
	// migration, not matched exactly: others may have been added.
	Subset map[string]bool `json:"subset,omitempty"`
}

// TakeSnapshot reads the tables of the tenants named.
//
// It reads a tenant's tables in one query, and the global tables in another:
// the seeded-migrations walk snapshots every table twice per migration, and
// a query per table per tenant made the round trips, not the rows, most of
// what a snapshot cost.
func TakeSnapshot(ctx context.Context, app *db.DB, tenants []uuid.UUID) (*Snapshot, error) {
	tables, err := Tables(ctx, app.Global())
	if err != nil {
		return nil, err
	}
	columns, err := tableColumns(ctx, app.Global())
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Columns: map[string][]string{}, Rows: map[string]map[uuid.UUID][]string{}}
	var global, tenanted []string
	for _, table := range tables {
		snap.Columns[table] = columns[table]
		snap.Rows[table] = map[uuid.UUID][]string{}
		if isGlobalTable(table) {
			global = append(global, table)
		} else {
			tenanted = append(tenanted, table)
		}
	}
	if err := readTables(ctx, app.Global(), global, uuid.Nil, snap.Rows); err != nil {
		return nil, err
	}
	for _, tenant := range tenants {
		if err := readTables(db.WithTenant(ctx, tenant), app, tenanted, tenant, snap.Rows); err != nil {
			return nil, err
		}
	}
	return snap, nil
}

// tableColumns are the columns of every table in the public schema, by
// table, each table's in name order.
func tableColumns(ctx context.Context, q db.Querier) (map[string][]string, error) {
	rows, err := q.Query(ctx,
		`SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = 'public' ORDER BY table_name, column_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string][]string{}
	for rows.Next() {
		var table, c string
		if err := rows.Scan(&table, &c); err != nil {
			return nil, err
		}
		cols[table] = append(cols[table], c)
	}
	return cols, rows.Err()
}

// readTables files the rows of the tables, as JSON and sorted, under tenant
// in into: one query, each row tagged with the table it came from.
func readTables(ctx context.Context, q db.Querier, tables []string, tenant uuid.UUID, into map[string]map[uuid.UUID][]string) error {
	if len(tables) == 0 {
		return nil
	}
	selects := make([]string, len(tables))
	for i, table := range tables {
		into[table][tenant] = nil
		// The names are the catalog's own (Tables), so quoting the literal is
		// for its quotes, not for untrusted input.
		selects[i] = fmt.Sprintf("SELECT '%s'::text, to_jsonb(t) FROM %s t", strings.ReplaceAll(table, "'", "''"), table)
	}
	rows, err := q.Query(ctx, strings.Join(selects, " UNION ALL "))
	if err != nil {
		return fmt.Errorf("read %s: %w", strings.Join(tables, ", "), err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var raw []byte
		if err := rows.Scan(&table, &raw); err != nil {
			return err
		}
		into[table][tenant] = append(into[table][tenant], string(raw))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, table := range tables {
		sort.Strings(into[table][tenant])
	}
	return nil
}

// Compare reports what a migration did to the rows that existed before it
// ran: each is still there, tenant for tenant, with the values of the
// columns it had. Columns the migration added are not compared; they are the
// migration's own to assert. What the note declares is allowed.
func Compare(before, after *Snapshot, note Note) []string {
	var problems []string
	for table, byTenant := range before.Rows {
		if _, ok := after.Rows[table]; !ok {
			if !slices.Contains(note.DroppedTables, table) {
				problems = append(problems, fmt.Sprintf("table %s was dropped with its rows, which the migration does not declare", table))
			}
			continue
		}
		if slices.Contains(note.RewrittenTables, table) {
			continue
		}
		renames := note.RenamedColumns[table]
		rewritten := note.RewrittenColumns[table]
		afterCols := map[string]bool{}
		for _, c := range after.Columns[table] {
			afterCols[c] = true
		}
		var kept []string
		for _, c := range before.Columns[table] {
			_, renamed := renames[c]
			switch {
			case slices.Contains(rewritten, c):
			case !afterCols[c] && !renamed:
				problems = append(problems, fmt.Sprintf("column %s.%s was dropped, which the migration does not declare", table, c))
			default:
				kept = append(kept, c)
			}
		}
		// Most tables are untouched by any one migration, and projecting means
		// decoding every row: rows identical as read, under the same columns,
		// project identically, so only the others are decoded. This was a third
		// of the seeded-migrations walk, which compares every row at every step.
		unchanged := len(renames) == 0 && slices.Equal(kept, after.Columns[table])
		for tenant, beforeRows := range byTenant {
			if unchanged && slices.Equal(beforeRows, after.Rows[table][tenant]) {
				continue
			}
			want := project(beforeRows, kept, nil)
			got := project(after.Rows[table][tenant], kept, renames)
			diff := diffRows
			if before.Subset[table] {
				diff = missingRows
			}
			if d := diff(want, got); d != "" {
				problems = append(problems, fmt.Sprintf("%s rows of tenant %s changed: %s", table, tenant, d))
			}
		}
	}
	return problems
}

// project reduces rows to the named columns, reading a renamed column from
// its new name, and returns them in canonical form.
func project(rows []string, columns []string, renames map[string]string) []string {
	out := make([]string, 0, len(rows))
	for _, raw := range rows {
		var full map[string]any
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&full); err != nil {
			panic(fmt.Errorf("seeddata: a row is not JSON: %w", err))
		}
		picked := map[string]any{}
		for _, c := range columns {
			if renamed, ok := renames[c]; ok {
				picked[c] = full[renamed]
				continue
			}
			if v, ok := full[c]; ok {
				picked[c] = v
			}
		}
		b, err := json.Marshal(picked)
		if err != nil {
			panic(err)
		}
		out = append(out, string(b))
	}
	sort.Strings(out)
	return out
}

func diffRows(want, got []string) string {
	if len(want) != len(got) {
		return fmt.Sprintf("%d rows before, %d after", len(want), len(got))
	}
	for i := range want {
		if want[i] != got[i] {
			return "a row differs in " + differingColumns(want[i], got[i]) + "\n  before: " + want[i] + "\n  after:  " + got[i]
		}
	}
	return ""
}

// missingRows is diffRows for a table that may have gained rows: each of
// want is in got, once per time it is in want.
func missingRows(want, got []string) string {
	have := map[string]int{}
	for _, g := range got {
		have[g]++
	}
	for _, w := range want {
		if have[w] == 0 {
			return "a row is gone or changed: " + w
		}
		have[w]--
	}
	return ""
}

func differingColumns(a, b string) string {
	var ma, mb map[string]any
	_ = json.Unmarshal([]byte(a), &ma)
	_ = json.Unmarshal([]byte(b), &mb)
	var cols []string
	for c, v := range ma {
		if fmt.Sprint(v) != fmt.Sprint(mb[c]) {
			cols = append(cols, c)
		}
	}
	sort.Strings(cols)
	return strings.Join(cols, ", ")
}
