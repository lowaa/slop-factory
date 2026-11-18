package ticket_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// sortOrders is slop-haus/testdata/ticket_sort_orders.json: a population
// with ties and NULLs and every sort's order of it, written out by hand. The browser's
// comparators are held to the same file (ticket_order.test.ts), which is
// what keeps a live change placed where the server would put it.
type sortOrders struct {
	Tickets []struct {
		ID        int64   `json:"id"`
		Priority  string  `json:"priority"`
		UpdatedAt string  `json:"updated_at"`
		ClosedAt  *string `json:"closed_at"`
	} `json:"tickets"`
	Orders map[string][]int64 `json:"orders"`
}

func loadSortOrders(t *testing.T) *sortOrders {
	t.Helper()
	raw, err := os.ReadFile("../../../testdata/ticket_sort_orders.json")
	require.NoError(t, err)
	var o sortOrders
	require.NoError(t, json.Unmarshal(raw, &o))
	return &o
}

// seed files the population, returning each fixture id's ticket id. Fixture
// ids rise in creation order, as ticket ids do, so the id tie-break agrees.
func (o *sortOrders) seed(t *testing.T, f *fixture) map[int64]int64 {
	t.Helper()
	ids := map[int64]int64{}
	for _, x := range o.Tickets {
		id := f.ticket(t, fmt.Sprintf("T%d", x.ID)).ID
		ids[x.ID] = id
		f.run(t, `UPDATE tickets SET priority = $2, updated_at = $3::timestamptz, closed_at = $4::timestamptz WHERE id = $1`,
			id, x.Priority, x.UpdatedAt, x.ClosedAt)
	}
	return ids
}

func TestEverySortWalksTheSameOrderAtEveryPageSize(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	o := loadSortOrders(t)
	ids := o.seed(t, f)

	assert.ElementsMatch(t, ticket.SortNames(), slices.Collect(maps.Keys(o.Orders)), "a registered sort has no expected order in the fixture")
	for _, name := range ticket.SortNames() {
		t.Run(name, func(t *testing.T) {
			want := make([]int64, len(o.Orders[name]))
			for i, id := range o.Orders[name] {
				want[i] = ids[id]
			}
			sort, err := ticket.SortByName(name)
			require.NoError(t, err)
			for _, size := range []int{1, 2, 5, len(want)} {
				got, _ := f.walk(t, ticket.ListFilter{Limit: size, Sort: sort})
				assert.Equal(t, want, got, "page size %d", size)
			}
		})
	}
}

// The recently_closed pages are read as two branches, the closed tickets and
// the NULL tail, so every page boundary is tried: before, on and after the
// last closed ticket, with ties on closed_at, whole pages of NULLs, and a
// cursor taken from a NULL ticket.
func TestRecentlyClosedWalksAcrossTheNullTailAtEveryPageSize(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	o := loadSortOrders(t)
	o.seed(t, f)
	sort, err := ticket.SortByName(ticket.SortRecentlyClosed)
	require.NoError(t, err)

	all, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{Limit: ticket.MaxPageSize, Sort: sort})
	require.NoError(t, err)
	require.Nil(t, all.Next)
	var want []int64
	nulls := 0
	for _, tk := range all.Tickets {
		want = append(want, tk.ID)
		if tk.ClosedAt == nil {
			nulls++
		}
	}
	require.GreaterOrEqual(t, nulls, 3, "the fixture needs a NULL tail longer than a small page")
	require.Less(t, nulls, len(want), "and closed tickets before it")
	for size := 1; size <= len(want)+1; size++ {
		got, _ := f.walk(t, ticket.ListFilter{Limit: size, Sort: sort})
		assert.Equal(t, want, got, "page size %d", size)
		got, _ = f.walk(t, ticket.ListFilter{Limit: size, Sort: sort, Summary: true})
		assert.Equal(t, want, got, "summary, page size %d", size)
	}
}

func TestACursorIsRejectedUnderEveryOtherSort(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for i := 0; i < 4; i++ {
		f.ticket(t, fmt.Sprintf("T%d", i))
	}
	for _, from := range ticket.SortNames() {
		fromSort, err := ticket.SortByName(from)
		require.NoError(t, err)
		page, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{Limit: 1, Sort: fromSort})
		require.NoError(t, err)
		cursor, err := ticket.DecodePageCursor(page.Next.Encode())
		require.NoError(t, err)
		for _, to := range ticket.SortNames() {
			toSort, err := ticket.SortByName(to)
			require.NoError(t, err)
			_, err = f.svc.ListPage(f.ctx, &ticket.ListFilter{Limit: 1, Sort: toSort, After: cursor})
			if from == to {
				assert.NoError(t, err, "%s onto itself", from)
			} else {
				assert.ErrorIs(t, err, ticket.ErrInvalidPage, "%s cursor under %s", from, to)
			}
		}
	}
}

type explainNode struct {
	NodeType  string        `json:"Node Type"`
	Relation  string        `json:"Parent Relationship"`
	IndexName string        `json:"Index Name"`
	Table     string        `json:"Relation Name"`
	Alias     string        `json:"Alias"`
	IndexCond string        `json:"Index Cond"`
	Plans     []explainNode `json:"Plans"`
}

func (n explainNode) walk(visit func(explainNode)) {
	visit(n)
	for _, c := range n.Plans {
		// The columns' own subqueries (whether an agent could take the
		// ticket) are not the order of the list.
		if c.Relation == "SubPlan" || c.Relation == "InitPlan" {
			continue
		}
		c.walk(visit)
	}
}

// Each sort is served by an index: a page is an ordered range read, never a
// sort of every ticket. The sequential scan is switched off so that a small
// table's planner preference does not hide a sort that has no index behind it
// -- without the index the plan has a Sort node whatever the setting.
func TestEverySortIsServedByAnIndex(t *testing.T) {
	t.Parallel()
	assertEverySortIsIndexed(t, ticket.ListFilter{}, false)
}

// The work columns list attempts. The attempts are read through the index on
// the work statuses, never a scan of every attempt, so a page costs what the
// live work does and not the tenant's history. The sorts by id are that
// index's own order, read forwards or backwards; the other sorts order by a
// ticket field, so a page of them is a sort of the live attempts, which is
// bounded by the live work and not by the board.
func TestEverySortIsServedByAnIndexForAttemptCards(t *testing.T) {
	t.Parallel()
	assertEverySortIsIndexed(t, ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusInProgress}, AttemptCards: true}, true)
}

func assertEverySortIsIndexed(t *testing.T, template ticket.ListFilter, cards bool) {
	f := newFixture(t)
	f.run(t, `INSERT INTO tickets (title, body, priority, status, created_at, updated_at, closed_at)
		SELECT 't' || g, '', (ARRAY['low','normal','high','urgent'])[g % 4 + 1], 'done',
		       now() - make_interval(secs => g), now() - make_interval(secs => g * 3 % 1000),
		       CASE WHEN g % 3 = 0 THEN NULL ELSE now() - make_interval(secs => g * 7 % 1000) END
		  FROM generate_series(1, 3000) g`)
	if cards {
		// Live attempts among a long done history, the shape of a board: the
		// in-progress ones are the minority a page must find without reading
		// the rest.
		f.run(t, `UPDATE tickets SET status = 'in_progress', closed_at = NULL WHERE id % 2 = 0`)
		f.run(t, `UPDATE attempts SET status = 'in_progress' WHERE ticket_id % 2 = 0`)
		f.run(t, `INSERT INTO attempts (ticket_id, number, status) SELECT id, 2, 'in_progress' FROM tickets WHERE id % 2 = 0`)
		f.run(t, `ANALYZE attempts`)
	}
	f.run(t, `ANALYZE tickets`)

	// The cursor of the tenth page of 200: a keyset read costs the same
	// there as at the top.
	deep := func(sort *ticket.Sort) *ticket.PageCursor {
		var after *ticket.PageCursor
		for range 10 {
			filter := template
			filter.Limit, filter.Sort, filter.After = 200, sort, after
			page, err := f.svc.ListPage(f.ctx, &filter)
			require.NoError(t, err)
			after = page.Next
			require.NotNil(t, after)
		}
		return after
	}
	for _, name := range ticket.SortNames() {
		sort, err := ticket.SortByName(name)
		require.NoError(t, err)
		for label, after := range map[string]*ticket.PageCursor{"first page": nil, "deep page": deep(sort)} {
			t.Run(name+" "+label, func(t *testing.T) {
				filter := template
				filter.Limit, filter.Sort, filter.After = 51, sort, after
				query, args, err := ticket.ListQuery(&filter)
				require.NoError(t, err)
				var raw string
				require.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error {
					// Remove the unordered access paths so the plan shows whether an ordered
					// index path exists; otherwise a bitmap scan + sort can win on cost
					// estimates alone. enable_sort stays on so a real sort regression is seen.
					if _, err := q.Exec(f.ctx, `SET LOCAL enable_seqscan = off`); err != nil {
						return err
					}
					if _, err := q.Exec(f.ctx, `SET LOCAL enable_bitmapscan = off`); err != nil {
						return err
					}
					return q.QueryRow(f.ctx, `EXPLAIN (COSTS OFF, FORMAT JSON) `+query, args...).Scan(&raw)
				}))
				var plans []struct {
					Plan explainNode `json:"Plan"`
				}
				require.NoError(t, json.Unmarshal([]byte(raw), &plans), raw)
				indexed := false
				scans, limits := 0, 0
				plans[0].Plan.walk(func(n explainNode) {
					if n.NodeType == "Limit" {
						limits++
					}
					isSort := strings.HasPrefix(n.NodeType, "Sort") || n.NodeType == "Incremental Sort"
					byID := name == ticket.SortNewest || name == ticket.SortOldest
					if !cards || byID {
						assert.False(t, isSort, "sorts the tickets: %s", raw)
					}
					if cards && n.Alias == "pa" {
						assert.True(t, strings.HasPrefix(n.NodeType, "Index"), "reads the attempts without an index: %s", raw)
						assert.Equal(t, "attempts_by_work_status", n.IndexName, "the attempts are not read through the index on their status: %s", raw)
					}
					// The ticket's attempt is joined in by its own index, one
					// row per ticket; the order is the tickets' scan.
					if strings.HasPrefix(n.NodeType, "Index") && n.Table == "tickets" {
						indexed = true
						scans++
						// A page past the first starts its index range at the
						// cursor; reading the index from the top and discarding
						// down to it would pass the checks above.
						// The NULL tail of a nullable first key is a range of
						// its own: the NULLs, from the top of their id order.
						bound := `[<>]=? `
						if name == ticket.SortRecentlyClosed {
							bound += `| IS NULL`
						}
						if after != nil && (!cards || byID) {
							assert.Regexp(t, bound, n.IndexCond, "the cursor does not bound the index range: %s", raw)
						}
					}
				})
				assert.True(t, indexed, "no index scan: %s", raw)
				// A page read as several ranges stops each at a page, as well as
				// the merge of them: without that each reads its whole range.
				assert.GreaterOrEqual(t, limits, scans, "a range is read past a page: %s", raw)
			})
		}
	}
}
