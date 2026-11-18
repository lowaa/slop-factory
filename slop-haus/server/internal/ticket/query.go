package ticket

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The ticket query contract: every list of tickets is a Filter (ListFilter,
// compiled by ticketPredicate), a Sort (from the registry below) and a Page
// (a limit and a keyset cursor). A client names a sort; it never sends a
// column or SQL.

const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// TimeRange bounds a timestamp to [From, Before); either may be nil.
type TimeRange struct {
	From, Before *time.Time
}

type KeyKind int

const (
	KeyInt KeyKind = iota
	KeyTime
)

// SortKey is one column of a sort's order.
type SortKey struct {
	Column string
	// Expr is the SQL the key orders by, for a key that is computed from
	// the column; empty is the column itself. An index serving the sort must
	// be on exactly this expression.
	Expr string
	Desc bool
	// Nullable columns sort NULLS LAST in either direction.
	Nullable bool
	Kind     KeyKind
	// Value reads the key from a ticket, to build the cursor.
	Value func(*Ticket) any
}

// Sort is a named order. Its last key must be unique (the id), so that two
// tickets never tie and a cursor names exactly one position. A ticket whose
// key changes while a client is paging (a reopened ticket's closed_at) may be
// skipped or seen twice in that walk, as with any keyset.
type Sort struct {
	Name string
	Keys []SortKey
	// Column is a value the order computes that the page then reports, read
	// in the same statement; nil for a sort that orders by stored columns.
	Column *ComputedColumn
}

// ComputedColumn is a SQL expression over the tickets table aliased t,
// selected as one more column of a list and scanned into the ticket.
type ComputedColumn struct {
	SQL  string
	Dest func(*Ticket) any
}

var idKey = func(desc bool) SortKey {
	return SortKey{Column: "id", Desc: desc, Kind: KeyInt, Value: func(t *Ticket) any { return t.ID }}
}

const (
	SortNewest          = "newest"
	SortOldest          = "oldest"
	SortRecentlyUpdated = "recently_updated"
	SortRecentlyClosed  = "recently_closed"
	SortPriority        = "priority"
)

// priorityRankSQL is a priority's place in the urgent-first order. The
// priority column is text whose alphabetical order is not the urgency order,
// so the sort computes the rank, and tickets_by_priority (migration tickets_sort_indexes) is an
// index on this exact expression: change one and the other.
const priorityRankSQL = `(CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END)`

func priorityRank(p model.Priority) int64 {
	switch p {
	case model.PriorityUrgent:
		return 0
	case model.PriorityHigh:
		return 1
	case model.PriorityNormal:
		return 2
	}
	return 3
}

// sortRegistry is the whole set of orders a client may ask for. Adding an
// order is adding an entry here, plus an index serving it.
var sortRegistry = []*Sort{
	{Name: SortNewest, Keys: []SortKey{idKey(true)}},
	{Name: SortOldest, Keys: []SortKey{idKey(false)}},
	{Name: SortRecentlyUpdated, Keys: []SortKey{
		{Column: "updated_at", Desc: true, Kind: KeyTime, Value: func(t *Ticket) any { return &t.UpdatedAt }},
		idKey(true),
	}},
	// Urgent first; within a priority the newest, as the board reads.
	{Name: SortPriority, Keys: []SortKey{
		{Expr: priorityRankSQL, Kind: KeyInt, Value: func(t *Ticket) any { return priorityRank(t.Priority) }},
		idKey(true),
	}},
	// The change most likely to still be built on is the one most worth
	// catching a problem in, so the awaiting-review list starts there.
	{Name: SortRecentlyClosed, Keys: []SortKey{
		{Column: "closed_at", Desc: true, Nullable: true, Kind: KeyTime, Value: func(t *Ticket) any { return t.ClosedAt }},
		idKey(true),
	}},
}

// unblocksSQL counts the unfinished tickets that wait on t through unfinished
// tickets only, which is Unblocks in the blocking graph (loadUnblocks is the
// same count for a set of tickets at once). A finished ticket holds nothing
// up. It is a recursive walk down from t, so it costs what t's dependants
// do and not the size of the board; a list that sorts by it should be
// narrowed to blocked tickets first.
const unblocksSQL = `(CASE WHEN t.status ` + unfinishedSQL + ` THEN (
	SELECT count(*) FROM (
		WITH RECURSIVE down(id) AS (
			SELECT d.ticket_id
			  FROM ticket_deps d
			  JOIN tickets x ON x.tenant_id = d.tenant_id AND x.id = d.ticket_id
			 WHERE d.tenant_id = t.tenant_id AND d.depends_on_ticket_id = t.id AND x.status ` + unfinishedSQL + `
			UNION
			SELECT d.ticket_id
			  FROM down
			  JOIN ticket_deps d ON d.tenant_id = t.tenant_id AND d.depends_on_ticket_id = down.id
			  JOIN tickets x ON x.tenant_id = d.tenant_id AND x.id = d.ticket_id
			 WHERE x.status ` + unfinishedSQL + `
		)
		SELECT id FROM down
	) reached
) ELSE 0 END)`

// blockingSort ranks tickets by how much finishing each would free, the one
// that frees most first. It is not in the registry: a client does not ask for
// it by name, because it is only meaningful over the blocked tickets and only
// BlockedPage runs it (cost: see unblocksSQL).
var blockingSort = &Sort{
	Name: "unblocks",
	Keys: []SortKey{
		{Expr: unblocksSQL, Desc: true, Kind: KeyInt, Value: func(t *Ticket) any { return int64(t.Unblocks) }},
		idKey(true),
	},
	Column: &ComputedColumn{SQL: unblocksSQL, Dest: func(t *Ticket) any { return &t.Unblocks }},
}

// attemptKey is the tie-break of a list of attempt cards: two cards of one
// ticket differ only by attempt number. It runs the way the id does, so the
// whole order reverses together ("newest" lists a ticket's newest attempt
// first) and the attempts index, which is in (ticket, attempt) order, serves
// the id sorts read forwards or backwards.
func attemptKey(desc bool) SortKey {
	return SortKey{Column: "attempt_number", Expr: "pa.number", Desc: desc, Kind: KeyInt,
		Value: func(t *Ticket) any { return int64(t.Card.Number) }}
}

// forCards is the sort of a list of attempt cards: s, then the attempt number.
func (s *Sort) forCards() *Sort {
	keys := append(slices.Clone(s.Keys), attemptKey(s.Keys[len(s.Keys)-1].Desc))
	return &Sort{Name: s.Name, Keys: keys, Column: s.Column}
}

// SortNames are the registered sorts, for a client to choose from.
func SortNames() []string {
	names := make([]string, len(sortRegistry))
	for i, s := range sortRegistry {
		names[i] = s.Name
	}
	return names
}

// SortByName resolves a client's sort name; empty means the default for the
// filter.
func SortByName(name string) (*Sort, error) {
	for _, s := range sortRegistry {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, fmt.Errorf("%w: unknown sort %q, one of %s", ErrInvalidPage, name, strings.Join(SortNames(), ", "))
}

// listSort is the order a read of f uses: the filter's sort, with the attempt
// number as its last key when the rows are attempt cards.
func (f *ListFilter) listSort() *Sort {
	if f.AttemptCards {
		return f.sort().forCards()
	}
	return f.sort()
}

func (f *ListFilter) sort() *Sort {
	if f.Sort != nil {
		return f.Sort
	}
	if f.AwaitingPostReview {
		s, _ := SortByName(SortRecentlyClosed)
		return s
	}
	s, _ := SortByName(SortNewest)
	return s
}

// sql is the expression the key compares and orders by, on the tickets table
// aliased t.
func (k SortKey) sql() string {
	if k.Expr != "" {
		return k.Expr
	}
	return "t." + k.Column
}

func (s *Sort) orderBy() string { return s.orderByOf("t") }

// orderByOf is orderBy over rows aliased alias, for the outer query of a page
// that is read as a union of branches (see afterBranches). The branches select
// plain columns, so only a key that is one can be ordered there.
func (s *Sort) orderByOf(alias string) string {
	parts := make([]string, len(s.Keys))
	for i, k := range s.Keys {
		parts[i] = k.sql()
		if alias != "t" {
			parts[i] = alias + "." + k.Column
		}
		if k.Desc {
			parts[i] += " DESC"
		}
		if k.Nullable {
			parts[i] += " NULLS LAST"
		}
	}
	return strings.Join(parts, ", ")
}

// blockedPredicate is the claim path's rule for a blocked ticket: a blocker
// counts only while it is unfinished. A finished ticket is never blocked, as
// in the graph, whose blockers of one it does not draw.
const blockedPredicate = `t.status ` + unfinishedSQL + ` AND EXISTS (
	SELECT 1 FROM ticket_deps d
	  JOIN tickets b ON b.tenant_id = d.tenant_id AND b.id = d.depends_on_ticket_id
	 WHERE d.tenant_id = t.tenant_id AND d.ticket_id = t.id AND b.status ` + unfinishedSQL + `)`

// ticketPredicate is the only place a ListFilter becomes SQL: a boolean
// expression over TicketFrom (the ticket as t, its attempt as pa), with placeholders numbered
// from argBase+1, and the arguments for them. Other stores embed it as a
// join condition or subquery. It ignores Sort, After, Limit and Summary,
// which are about the page and not about which tickets match.
func ticketPredicate(f *ListFilter, argBase int) (string, []any) {
	var conds []string
	var args []any
	arg := func(v any) int {
		args = append(args, v)
		return argBase + len(args)
	}
	// An attempt card is in the column of its attempt's status, which cardFrom
	// joins on; the ticket's own status says nothing about it.
	if len(f.Statuses) > 0 && !f.AttemptCards {
		conds = append(conds, fmt.Sprintf(`t.status = ANY($%d)`, arg(stringsOf(f.Statuses))))
	}
	if len(f.Priorities) > 0 {
		conds = append(conds, fmt.Sprintf(`t.priority = ANY($%d)`, arg(stringsOf(f.Priorities))))
	}
	if len(f.IDs) > 0 {
		conds = append(conds, fmt.Sprintf(`t.id = ANY($%d)`, arg(f.IDs)))
	}
	if f.AwaitingPostReview {
		conds = append(conds, awaitingPostReview)
	}
	if f.Blocked {
		conds = append(conds, blockedPredicate)
	}
	for _, r := range []struct {
		column string
		bounds TimeRange
	}{{"closed_at", f.Closed}, {"created_at", f.Created}, {"updated_at", f.Updated}} {
		if r.bounds.From != nil {
			conds = append(conds, fmt.Sprintf(`t.%s >= $%d`, r.column, arg(*r.bounds.From)))
		}
		if r.bounds.Before != nil {
			conds = append(conds, fmt.Sprintf(`t.%s < $%d`, r.column, arg(*r.bounds.Before)))
		}
	}
	if f.PRLink != "" {
		like := fmt.Sprintf(`LIKE $%d ESCAPE '\'`, arg(likePrefix(f.PRLink)))
		if f.AttemptCards {
			// The card's own attempt, which is what its PR link shows.
			conds = append(conds, `lower(pa.pr_url) `+like)
		} else {
			conds = append(conds, promotedAttemptHas(`lower(pp.pr_url) `+like))
		}
	}
	for _, term := range searchTerms(f.Text) {
		match := textMatch(arg(likeContains(term)), f.TextSkipsAttempts)
		// "#85" is how the board and the tickets' own text write a
		// reference, so it also finds ticket 85 even when no text says "#85".
		if id, ok := ticketRef(term); ok {
			match = fmt.Sprintf(`(t.id = $%d OR %s)`, arg(id), match)
		}
		conds = append(conds, match)
	}
	if len(conds) == 0 {
		return "TRUE", nil
	}
	return strings.Join(conds, " AND "), args
}

// Predicate is ticketPredicate for stores outside this package that list
// something per ticket (LLM usage by ticket) and must filter tickets exactly
// as the ticket lists do. It reads only the ticket as t: the attempt's fields
// are reached through promotedAttemptHas, so any FROM naming tickets t will do.
func (f *ListFilter) Predicate(argBase int) (string, []any) {
	return ticketPredicate(f, argBase)
}

func stringsOf[T ~string](vs []T) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = string(v)
	}
	return out
}

// QueryHash is queryHash for a cursor kept by a store outside this package.
func (f *ListFilter) QueryHash() string { return f.queryHash() }

// queryHash identifies the query a cursor belongs to: the filter and the
// sort, but not the page size, so a client may change that mid-walk.
func (f *ListFilter) queryHash() string {
	statuses := stringsOf(f.Statuses)
	slices.Sort(statuses)
	priorities := stringsOf(f.Priorities)
	slices.Sort(priorities)
	ids := slices.Clone(f.IDs)
	slices.Sort(ids)
	instant := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	canonical, _ := json.Marshal([]any{
		f.sort().Name, statuses, priorities, ids, f.AwaitingPostReview, f.Blocked, f.PRLink,
		searchTerms(f.Text), f.TextSkipsAttempts,
		instant(f.Closed.From), instant(f.Closed.Before),
		instant(f.Created.From), instant(f.Created.Before),
		instant(f.Updated.From), instant(f.Updated.Before),
	})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:8])
}

// PageCursor is the position of the last ticket of a page. Clients only see
// it encoded, as an opaque string.
type PageCursor struct {
	// Sort and Query pin the cursor to the order and filter it was issued
	// for: replayed against another it would skip or repeat tickets, so
	// it is rejected instead.
	Sort  string
	Query string
	// Values are the last ticket's sort keys: int64 or *time.Time (nil for
	// a NULL) as the sort's key kinds say, after decoding json.Number or
	// string.
	Values []any
	// Cards says the page was a list of attempt cards, and Values then ends
	// with the last card's attempt number. The query hash leaves this out, so
	// a client repeating a column's filter with just the cursor reaches the
	// same list.
	Cards bool
}

func newPageCursor(f *ListFilter, last *Ticket) *PageCursor {
	sort := f.listSort()
	c := &PageCursor{Sort: sort.Name, Query: f.queryHash(), Cards: f.AttemptCards}
	for _, k := range sort.Keys {
		c.Values = append(c.Values, k.Value(last))
	}
	return c
}

type wireCursor struct {
	Sort   string `json:"s"`
	Query  string `json:"q"`
	Values []any  `json:"v"`
	Cards  bool   `json:"c,omitempty"`
}

func (c *PageCursor) Encode() string {
	values := make([]any, len(c.Values))
	for i, v := range c.Values {
		switch v := v.(type) {
		case *time.Time:
			if v != nil {
				values[i] = v.UTC().Format(time.RFC3339Nano)
			}
		default:
			values[i] = v
		}
	}
	raw, _ := json.Marshal(wireCursor{Sort: c.Sort, Query: c.Query, Values: values, Cards: c.Cards})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodePageCursor(s string) (*PageCursor, error) {
	bad := fmt.Errorf("%w: bad cursor", ErrInvalidPage)
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, bad
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var w wireCursor
	if err := dec.Decode(&w); err != nil || w.Sort == "" || len(w.Values) == 0 {
		return nil, bad
	}
	return &PageCursor{Sort: w.Sort, Query: w.Query, Values: w.Values, Cards: w.Cards}, nil
}

// values types the cursor's values for sort, or rejects a cursor that does
// not fit it.
func (c *PageCursor) typed(sort *Sort) ([]any, error) {
	bad := fmt.Errorf("%w: bad cursor", ErrInvalidPage)
	if len(c.Values) != len(sort.Keys) {
		return nil, bad
	}
	out := make([]any, len(c.Values))
	for i, k := range sort.Keys {
		switch v := c.Values[i].(type) {
		case nil:
			if !k.Nullable {
				return nil, bad
			}
		case int64:
			if k.Kind != KeyInt {
				return nil, bad
			}
			out[i] = v
		case json.Number:
			n, err := strconv.ParseInt(v.String(), 10, 64)
			if k.Kind != KeyInt || err != nil {
				return nil, bad
			}
			out[i] = n
		case *time.Time:
			// A NULL key, as newPageCursor makes it before the wire form
			// (a nil pointer in an interface is not a nil interface).
			switch {
			case k.Kind != KeyTime:
				return nil, bad
			case v == nil:
				if !k.Nullable {
					return nil, bad
				}
			default:
				out[i] = *v
			}
		case string:
			t, err := time.Parse(time.RFC3339Nano, v)
			if k.Kind != KeyTime || err != nil {
				return nil, bad
			}
			out[i] = t
		default:
			return nil, bad
		}
	}
	return out, nil
}

// afterBranches is the keyset condition for the rows after c in the filter's
// sort, as one condition per branch of the page, in sort order. Each branch is
// an ordered read of its own, and the page is their concatenation (listQuery).
//
// A branch has one disjunct per key, each holding the keys before it equal and
// that one past the cursor. A keyset, not an offset, so a page costs the same
// however far down the list it is and a ticket changing meanwhile does not
// shift the next page. Written out per key rather than as a (a, b) < ($1, $2)
// row comparison, which cannot mix directions or order NULLs last.
//
// An OR is not a range the index can start in, so a deep page would read and
// sort every ticket. The first key alone bounds the rows from the cursor's
// value onward, redundantly, and that bound is the index's range. A nullable
// first key cannot take it, because its NULLs sort last, past the range, and
// `col <= $x OR col IS NULL` is not a range either (tried). So the rows with a
// value and the NULL tail are separate branches, each of which is a range: the
// bounded disjuncts, then every NULL row. A cursor already in the NULL tail
// has only that tail left, which the disjuncts below express as a range.
func afterBranches(f *ListFilter, c *PageCursor, argBase int) ([]string, []any, error) {
	sort := f.listSort()
	if c.Sort != sort.Name || c.Query != f.queryHash() || c.Cards != f.AttemptCards {
		return nil, nil, fmt.Errorf("%w: cursor is for a different query", ErrInvalidPage)
	}
	values, err := c.typed(sort)
	if err != nil {
		return nil, nil, err
	}
	var args []any
	pos := make([]int, len(sort.Keys))
	for i, v := range values {
		if v != nil {
			args = append(args, v)
			pos[i] = argBase + len(args)
		}
	}
	first := sort.Keys[0]
	// The NULL tail is a branch of its own only while the cursor is before it.
	// The branches are merged by column name, so a nullable key computed from
	// an expression keeps the single branch: correct, but without an index range
	// past the first page.
	split := first.Nullable && first.Expr == "" && values[0] != nil
	var disjuncts []string
	for i, k := range sort.Keys {
		if values[i] == nil {
			// Nothing sorts after NULL, which is last.
			continue
		}
		col := k.sql()
		cmp := ">"
		if k.Desc {
			cmp = "<"
		}
		past := fmt.Sprintf(`%s %s $%d`, col, cmp, pos[i])
		if k.Nullable && !(split && i == 0) {
			past = fmt.Sprintf(`(%s OR %s IS NULL)`, past, col)
		}
		parts := []string{past}
		for j := 0; j < i; j++ {
			if values[j] == nil {
				parts = append([]string{sort.Keys[j].sql() + ` IS NULL`}, parts...)
			} else {
				parts = append([]string{fmt.Sprintf(`%s = $%d`, sort.Keys[j].sql(), pos[j])}, parts...)
			}
		}
		disjuncts = append(disjuncts, "("+strings.Join(parts, " AND ")+")")
	}
	if len(disjuncts) == 0 {
		return []string{"FALSE"}, nil, nil
	}
	keyset := "(" + strings.Join(disjuncts, " OR ") + ")"
	if values[0] != nil && (split || !first.Nullable) {
		cmp := ">="
		if first.Desc {
			cmp = "<="
		}
		keyset = fmt.Sprintf(`%s %s $%d AND %s`, first.sql(), cmp, pos[0], keyset)
	}
	if split {
		return []string{keyset, first.sql() + ` IS NULL`}, args, nil
	}
	return []string{keyset}, args, nil
}

// Page is one page of a list; Next is nil on the last.
type Page struct {
	Tickets []Ticket
	Next    *PageCursor
}
