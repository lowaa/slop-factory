package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	settings "github.com/lowaa/slop-factory/go-settings"
)

const columns = `setting_name, setting_type, str_value, int_value, bool_value, float_value, created_at`

// AppendSQL inserts one row for the key ($1) and returns it; a table with an
// actor column takes the actor as $8. For a table with
// a membership rule it inserts nothing, so returns pgx.ErrNoRows, when the
// key is no member.
func (t *Table) AppendSQL() string {
	if t.membershipTable != "" {
		return `
		INSERT INTO ` + t.name + ` (` + t.keyCol + `, setting_name, setting_type, str_value, int_value, bool_value, float_value)
		SELECT m.user_id, $2, $3, $4, $5, $6, $7
		  FROM ` + t.membershipTable + ` m
		 WHERE m.tenant = current_tenant() AND m.user_id = $1
		RETURNING ` + columns
	}
	if t.actorCol != "" {
		return `
		INSERT INTO ` + t.name + ` (` + t.keyCol + `, setting_name, setting_type, str_value, int_value, bool_value, float_value, ` + t.actorCol + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING ` + columns
	}
	return `
		INSERT INTO ` + t.name + ` (` + t.keyCol + `, setting_name, setting_type, str_value, int_value, bool_value, float_value)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING ` + columns
}

// LatestSQL seeks the newest row per name ($2) for the key ($1) through the
// table's index, so its cost is the number of known settings however long a
// history grows. DISTINCT ON over the key's rows would read all of it.
func (t *Table) LatestSQL() string {
	from, where := `t`, `t.`+t.keyCol+` = $1`
	if t.membershipTable != "" {
		// Someone who left and rejoined starts again from the defaults, so an
		// old value cannot switch itself back on.
		from = `t JOIN ` + t.membershipTable + ` m ON m.tenant = t.tenant_id AND m.user_id = t.user_id`
		where += ` AND t.created_at >= m.created_at`
	}
	return `
	SELECT s.setting_name, s.setting_type, s.str_value, s.int_value, s.bool_value, s.float_value, s.created_at
	  FROM unnest($2::text[]) AS n(setting_name)
	 CROSS JOIN LATERAL (
	       SELECT t.* FROM ` + t.name + ` ` + from + `
	        WHERE ` + where + ` AND t.setting_name = n.setting_name
	        ORDER BY ` + t.latestOrder + `
	        LIMIT 1) s`
}

// HistorySQL pages the key's ($1) rows for a name ($2), newest first, those
// with an id below $3 (0 for the newest), at most $4.
func (t *Table) HistorySQL() string {
	earlier, from := `false`, `t`
	if t.membershipTable != "" {
		earlier = `t.created_at < m.created_at`
		from = `t JOIN ` + t.membershipTable + ` m ON m.tenant = t.tenant_id AND m.user_id = t.user_id`
	}
	actor, actorDeleted := `NULL::uuid`, `false`
	if t.actorCol != "" {
		actor, actorDeleted = `t.`+t.actorCol, `t.`+t.actorDeletedCol
	}
	return `
	SELECT t.id, t.setting_type, t.str_value, t.int_value, t.bool_value, t.float_value, t.created_at, ` + earlier + `, ` + actor + `, ` + actorDeleted + `
	  FROM ` + t.name + ` ` + from + `
	 WHERE t.` + t.keyCol + ` = $1 AND t.setting_name = $2 AND ($3::bigint = 0 OR t.id < $3)
	 ORDER BY t.id DESC
	 LIMIT $4`
}

// Append writes a new row. The caller has already checked name and v against
// its registry, and has scoped q (tenant, user) as its scope requires.
func (t *Table) Append(ctx context.Context, q Querier, key uuid.UUID, name string, v settings.Value) (*settings.Stored, error) {
	return t.append(ctx, q, key, name, v, nil)
}

// AppendBy is Append for a table that records who made the change. The caller
// passes the authenticated actor: the database does not check that by is the
// person making the request, only that it is a user.
func (t *Table) AppendBy(ctx context.Context, q Querier, key uuid.UUID, name string, v settings.Value, by uuid.UUID) (*settings.Stored, error) {
	if t.actorCol == "" {
		return nil, fmt.Errorf("pgstore: %s does not record who changed a setting", t.name)
	}
	if by == uuid.Nil {
		return nil, errors.New("pgstore: no actor")
	}
	return t.append(ctx, q, key, name, v, &by)
}

func (t *Table) append(ctx context.Context, q Querier, key uuid.UUID, name string, v settings.Value, by *uuid.UUID) (*settings.Stored, error) {
	str, i, b, f, err := storedColumns(v)
	if err != nil {
		return nil, err
	}
	args := []any{key, name, string(v.Type), str, i, b, f}
	if t.actorCol != "" {
		args = append(args, by)
	}
	row, err := scanStored(q.QueryRow(ctx, t.AppendSQL(), args...))
	if err != nil {
		return nil, fmt.Errorf("append %q to %s: %w", name, t.name, err)
	}
	return row, nil
}

// Latest returns the newest row of each of the given names; a name with no
// row is absent.
func (t *Table) Latest(ctx context.Context, q Querier, key uuid.UUID, names []string) ([]settings.Stored, error) {
	rows, err := q.Query(ctx, t.LatestSQL(), key, names)
	if err != nil {
		return nil, fmt.Errorf("latest of %s: %w", t.name, err)
	}
	defer rows.Close()
	var out []settings.Stored
	for rows.Next() {
		st, err := scanStored(rows)
		if err != nil {
			return nil, fmt.Errorf("latest of %s: %w", t.name, err)
		}
		out = append(out, *st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("latest of %s: %w", t.name, err)
	}
	return out, nil
}

// History returns the key's rows for a name, newest first, those with an id
// below before (0 for the newest), at most limit.
func (t *Table) History(ctx context.Context, q Querier, key uuid.UUID, name string, before int64, limit int) ([]settings.Change, error) {
	rows, err := q.Query(ctx, t.HistorySQL(), key, name, before, limit)
	if err != nil {
		return nil, fmt.Errorf("history of %q in %s: %w", name, t.name, err)
	}
	defer rows.Close()
	var out []settings.Change
	for rows.Next() {
		var (
			c   settings.Change
			typ string
			str *string
			i   *int64
			b   *bool
			f   *float64
		)
		if err := rows.Scan(&c.ID, &typ, &str, &i, &b, &f, &c.CreatedAt, &c.EarlierMembership, &c.ChangedBy, &c.ChangedByDeleted); err != nil {
			return nil, fmt.Errorf("history of %q in %s: %w", name, t.name, err)
		}
		c.Value = valueOf(settings.Type(typ), str, i, b, f)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("history of %q in %s: %w", name, t.name, err)
	}
	return out, nil
}

// storedColumns picks the one value column a row of v's type fills.
func storedColumns(v settings.Value) (str *string, i *int64, b *bool, f *float64, err error) {
	switch v.Type {
	case settings.TypeStr:
		str = &v.Str
	case settings.TypeInt:
		i = &v.Int
	case settings.TypeBool:
		b = &v.Bool
	case settings.TypeFloat:
		f = &v.Float
	default:
		err = fmt.Errorf("pgstore: cannot store type %q", v.Type)
	}
	return
}

func scanStored(row pgx.Row) (*settings.Stored, error) {
	var (
		st  settings.Stored
		typ string
		str *string
		i   *int64
		b   *bool
		f   *float64
	)
	if err := row.Scan(&st.Name, &typ, &str, &i, &b, &f, &st.CreatedAt); err != nil {
		return nil, err
	}
	st.Value = valueOf(settings.Type(typ), str, i, b, f)
	return &st, nil
}

// valueOf rebuilds a Value from a row. The tables' CHECKs guarantee the column
// the type names is the one set.
func valueOf(t settings.Type, str *string, i *int64, b *bool, f *float64) settings.Value {
	v := settings.Value{Type: t}
	switch t {
	case settings.TypeStr:
		v.Str = deref(str)
	case settings.TypeInt:
		v.Int = deref(i)
	case settings.TypeBool:
		v.Bool = deref(b)
	case settings.TypeFloat:
		v.Float = deref(f)
	}
	return v
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// Runner runs fn with a Querier scoped as the app scopes a request: the
// context's tenant set, and for a per-user table the current user. The
// Querier is only valid inside fn.
type Runner func(ctx context.Context, fn func(q Querier) error) error

// Store is a settings.Store over a Table, each call running in its own
// scope through the Runner. An app whose handlers compose several queries in
// one transaction uses the Table directly with that transaction's Querier.
type Store struct {
	table *Table
	run   Runner
}

var (
	_ settings.Store      = (*Store)(nil)
	_ settings.ActorStore = (*Store)(nil)
)

func NewStore(table *Table, run Runner) *Store { return &Store{table: table, run: run} }

func (s *Store) Append(ctx context.Context, key uuid.UUID, name string, v settings.Value) (*settings.Stored, error) {
	var out *settings.Stored
	err := s.run(ctx, func(q Querier) (err error) {
		out, err = s.table.Append(ctx, q, key, name, v)
		return err
	})
	return out, err
}

func (s *Store) AppendBy(ctx context.Context, key uuid.UUID, name string, v settings.Value, by uuid.UUID) (*settings.Stored, error) {
	var out *settings.Stored
	err := s.run(ctx, func(q Querier) (err error) {
		out, err = s.table.AppendBy(ctx, q, key, name, v, by)
		return err
	})
	return out, err
}

func (s *Store) Latest(ctx context.Context, key uuid.UUID, names []string) ([]settings.Stored, error) {
	var out []settings.Stored
	err := s.run(ctx, func(q Querier) (err error) {
		out, err = s.table.Latest(ctx, q, key, names)
		return err
	})
	return out, err
}

func (s *Store) History(ctx context.Context, key uuid.UUID, name string, before int64, limit int) ([]settings.Change, error) {
	var out []settings.Change
	err := s.run(ctx, func(q Querier) (err error) {
		out, err = s.table.History(ctx, q, key, name, before, limit)
		return err
	})
	return out, err
}
