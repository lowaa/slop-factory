package seeddata

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func snap(columns map[string][]string, rows map[string]map[uuid.UUID][]string) *Snapshot {
	return &Snapshot{Columns: columns, Rows: rows}
}

var tenant = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func twoRows() *Snapshot {
	return snap(map[string][]string{"things": {"id", "name"}},
		map[string]map[uuid.UUID][]string{"things": {tenant: {`{"id":1,"name":"a"}`, `{"id":2,"name":"b"}`}}})
}

func TestCompareAcceptsAMigrationThatLeavesRowsAlone(t *testing.T) {
	after := snap(map[string][]string{"things": {"id", "name", "added"}},
		map[string]map[uuid.UUID][]string{"things": {tenant: {`{"added":null,"id":1,"name":"a"}`, `{"added":"x","id":2,"name":"b"}`}}})
	assert.Empty(t, Compare(twoRows(), after, Note{}), "a column the migration added is its own business")
}

// Most tables at most steps: the rows are compared as read, undecoded. Rows
// that are not JSON show it, since decoding one panics.
func TestCompareAcceptsATableTheMigrationDidNotTouch(t *testing.T) {
	undecodable := func() *Snapshot {
		return snap(map[string][]string{"things": {"id", "name"}},
			map[string]map[uuid.UUID][]string{"things": {tenant: {"not json 1", "not json 2"}}})
	}
	assert.NotPanics(t, func() {
		assert.Empty(t, Compare(undecodable(), undecodable(), Note{}))
	})
}

// Rows identical as read are not enough when the note renames a column: the
// renamed column's values are looked for under the new name.
func TestCompareChecksARenameEvenOverIdenticalRows(t *testing.T) {
	note := Note{RenamedColumns: map[string]map[string]string{"things": {"name": "title"}}}
	problems := Compare(twoRows(), twoRows(), note)
	if assert.NotEmpty(t, problems) {
		assert.Contains(t, problems[0], "a row differs in name")
	}
}

func TestCompareReportsWhatAMigrationDoesToExistingRows(t *testing.T) {
	cols := map[string][]string{"things": {"id", "name"}}
	for name, tc := range map[string]struct {
		after *Snapshot
		note  Note
		want  string
	}{
		"a lost row": {
			after: snap(cols, map[string]map[uuid.UUID][]string{"things": {tenant: {`{"id":1,"name":"a"}`}}}),
			want:  "2 rows before, 1 after",
		},
		"a changed value": {
			after: snap(cols, map[string]map[uuid.UUID][]string{"things": {tenant: {`{"id":1,"name":"a"}`, `{"id":2,"name":"CHANGED"}`}}}),
			want:  "a row differs in name",
		},
		"a dropped column": {
			after: snap(map[string][]string{"things": {"id"}}, map[string]map[uuid.UUID][]string{"things": {tenant: {`{"id":1}`, `{"id":2}`}}}),
			want:  "column things.name was dropped",
		},
		"a dropped table": {
			after: snap(map[string][]string{}, map[string]map[uuid.UUID][]string{}),
			want:  "table things was dropped",
		},
	} {
		t.Run(name, func(t *testing.T) {
			problems := Compare(twoRows(), tc.after, Note{})
			if assert.NotEmpty(t, problems) {
				assert.Contains(t, problems[0], tc.want)
			}
		})
	}
}

func TestCompareAllowsWhatTheNoteDeclares(t *testing.T) {
	dropped := snap(map[string][]string{}, map[string]map[uuid.UUID][]string{})
	assert.Empty(t, Compare(twoRows(), dropped, Note{DroppedTables: []string{"things"}}))

	rewritten := snap(map[string][]string{"things": {"id"}}, map[string]map[uuid.UUID][]string{"things": {tenant: {`{"id":1}`, `{"id":2}`}}})
	assert.Empty(t, Compare(twoRows(), rewritten, Note{RewrittenColumns: map[string][]string{"things": {"name"}}}))

	renamed := snap(map[string][]string{"things": {"id", "title"}},
		map[string]map[uuid.UUID][]string{"things": {tenant: {`{"id":1,"title":"a"}`, `{"id":2,"title":"b"}`}}})
	note := Note{RenamedColumns: map[string]map[string]string{"things": {"name": "title"}}}
	assert.Empty(t, Compare(twoRows(), renamed, note))

	renamedWrong := snap(map[string][]string{"things": {"id", "title"}},
		map[string]map[uuid.UUID][]string{"things": {tenant: {`{"id":1,"title":"a"}`, `{"id":2,"title":"x"}`}}})
	assert.NotEmpty(t, Compare(twoRows(), renamedWrong, note), "a rename must carry the values over")
}

func TestMissingSeedersNamesTheTablesNothingSeeds(t *testing.T) {
	seeders := Registry{"known": tenantSeeder(nil)}
	assert.Equal(t, []string{"new_table"}, MissingSeeders([]string{"known", "new_table"}, seeders))
	assert.Empty(t, MissingSeeders([]string{"known"}, seeders))
}

// withNotes swaps in the given notes for one test: the real map only holds
// the migrations after the baseline, and may hold none.
func withNotes(t *testing.T, notes map[int64]Note) {
	t.Helper()
	saved := Notes
	Notes = notes
	t.Cleanup(func() { Notes = saved })
}

func TestNotesBetweenMergesEveryKindOfAllowance(t *testing.T) {
	withNotes(t, map[int64]Note{
		9001: {RenamedColumns: map[string]map[string]string{"tickets": {"old_name": "new_name"}}},
		9002: {RewrittenColumns: map[string][]string{"agents": {"chat_cursor"}}},
		9003: {RewrittenTables: []string{"x"}, DroppedTables: []string{"gone"}},
	})
	merged := NotesBetween(9000, 9003)
	assert.Equal(t, "new_name", merged.RenamedColumns["tickets"]["old_name"])
	assert.Equal(t, []string{"chat_cursor"}, merged.RewrittenColumns["agents"])
	assert.Equal(t, []string{"x"}, merged.RewrittenTables)
	assert.Equal(t, []string{"gone"}, merged.DroppedTables)
}

func TestNotesBetweenTakesOnlyTheMigrationsInTheRange(t *testing.T) {
	withNotes(t, map[int64]Note{
		9001: {DroppedTables: []string{"a"}},
		9002: {DroppedTables: []string{"b", "c"}},
	})
	assert.Empty(t, NotesBetween(9002, 9003).DroppedTables, "9002 is already applied")
	assert.ElementsMatch(t, []string{"a", "b", "c"}, NotesBetween(9000, 9002).DroppedTables)
	assert.Empty(t, NotesBetween(9001, 9001).DroppedTables, "nothing is applied")
}
