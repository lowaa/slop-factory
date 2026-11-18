package change_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestLandingState_FromTheColumns(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		rec  change.Record
		want change.LandingState
	}{
		{"fresh", change.Record{}, change.LandingOpen},
		{"merge in flight", change.Record{Merging: true}, change.LandingMerging},
		{"merged", change.Record{MergedAt: &now}, change.LandingMerged},
		{"merged while claimed", change.Record{MergedAt: &now, Merging: true}, change.LandingMerged},
		{"closed", change.Record{ClosedAt: &now}, change.LandingClosed},
		{"superseded", change.Record{SupersededAt: &now}, change.LandingSuperseded},
		{"closed, then superseded", change.Record{ClosedAt: &now, SupersededAt: &now}, change.LandingSuperseded},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.rec.State(), tc.name)
	}
}

func TestLandingState_TableIsWellFormed(t *testing.T) {
	for _, s := range change.LandingStates() {
		assert.True(t, s.CanTransition(s))
	}
	assert.True(t, change.LandingMerged.Final())
	assert.True(t, change.LandingSuperseded.Final())
	assert.False(t, change.LandingOpen.Final())
}

// stateOf is the landing state of a change row, whether or not it is the
// attempt's live one.
func (f *fixture) stateOf(id int64) change.LandingState {
	f.t.Helper()
	var rec change.Record
	require.NoError(f.t, f.database.QueryRow(f.ctx, `
		SELECT superseded_at, merged_at, closed_at, COALESCE(merging_since > now() - interval '5 minutes', false)
		  FROM ticket_changes WHERE id = $1`, id).Scan(&rec.SupersededAt, &rec.MergedAt, &rec.ClosedAt, &rec.Merging))
	return rec.State()
}

// Every statement of the store that moves a change is run from every state
// it can start in: where it lands is in the table, and every edge of the
// table is landed on by some statement.
func TestLandingState_StoreMovesAreInTheTable(t *testing.T) {
	type env struct {
		f   *fixture
		h   *holder
		rec *change.Record
		ctx context.Context
	}
	setups := map[change.LandingState]func(*env){
		change.LandingOpen: func(*env) {},
		change.LandingMerging: func(e *env) {
			claim, err := e.f.store.BeginMerge(e.ctx, e.rec.ID)
			require.NoError(t, err)
			require.NotNil(t, claim)
		},
		change.LandingMerged: func(e *env) {
			ok, err := e.f.store.MarkMerged(e.ctx, e.rec.ID, e.h.agent, "sha")
			require.NoError(t, err)
			require.True(t, ok)
		},
		change.LandingClosed: func(e *env) {
			ok, err := e.f.store.MarkClosed(e.ctx, e.rec.ID, "closing_rule")
			require.NoError(t, err)
			require.True(t, ok)
		},
		change.LandingSuperseded: func(e *env) {
			_, err := e.f.openRecord(e.ctx, &change.Record{TicketID: e.h.ticket.ID, Change: ref(2), Head: "b", Base: "main", OpenedBy: e.h.agent})
			require.NoError(t, err)
		},
	}
	moves := map[string]func(*env){
		"begin merge": func(e *env) { _, _ = e.f.store.BeginMerge(e.ctx, e.rec.ID) },
		"merge claim expires": func(e *env) {
			_, err := e.f.database.Exec(e.ctx, `UPDATE ticket_changes SET merging_since = now() - interval '10 minutes' WHERE id = $1 AND merging_since IS NOT NULL`, e.rec.ID)
			require.NoError(t, err)
		},
		"mark merged": func(e *env) { _, _ = e.f.store.MarkMerged(e.ctx, e.rec.ID, e.h.agent, "sha2") },
		"mark closed": func(e *env) { _, _ = e.f.store.MarkClosed(e.ctx, e.rec.ID, "closing_rule") },
		"open another": func(e *env) {
			_, _ = e.f.openRecord(e.ctx, &change.Record{TicketID: e.h.ticket.ID, Change: ref(3), Head: "c", Base: "main", OpenedBy: e.h.agent})
		},
	}

	reached := map[[2]change.LandingState]bool{}
	for from, setup := range setups {
		for name, move := range moves {
			t.Run(string(from)+"/"+name, func(t *testing.T) {
				f := newFixture(t)
				e := &env{f: f, h: f.hold(false), ctx: f.ctx}
				rec, err := f.openRecord(f.ctx, &change.Record{TicketID: e.h.ticket.ID, Change: ref(1), Head: "a", Base: "main", OpenedBy: e.h.agent})
				require.NoError(t, err)
				e.rec = rec
				setup(e)
				require.Equal(t, from, f.stateOf(rec.ID))

				move(e)

				to := f.stateOf(rec.ID)
				assert.True(t, from.CanTransition(to), "%s -> %s by %s is not in the table", from, to, name)
				if to != from {
					reached[[2]change.LandingState{from, to}] = true
				}
			})
		}
	}

	for from, tos := range change.LandingTransitionsForTest() {
		for _, to := range tos {
			assert.True(t, reached[[2]change.LandingState{from, to}], "%s -> %s is in the table but no store statement makes it", from, to)
		}
	}
}

func TestLandingStateDiagramInTheDocs(t *testing.T) {
	path := "../../STATE_MACHINES.md"
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	want, ok := ticket.WithDiagram(string(raw), "landing", change.LandingStateDiagram())
	require.True(t, ok, "STATE_MACHINES.md has no landing diagram markers")
	if os.Getenv("UPDATE_DOCS") != "" {
		require.NoError(t, os.WriteFile(path, []byte(want), 0o644))
		return
	}
	assert.Equal(t, want, string(raw), "STATE_MACHINES.md is stale: run with UPDATE_DOCS=1")
}
