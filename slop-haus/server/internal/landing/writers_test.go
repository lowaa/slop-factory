package landing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writerPin says which transitions of the state table one top-level
// declaration's SQL writes on landings makes. A write nothing here accounts for
// is a way to move an entry that the table does not know about.
type writerPin struct {
	// moves are the table's transitions the statement makes.
	moves []phasePair
	// enters is the phase a new row is inserted in.
	enters Phase
	// neutral is a write that leaves the entry's phase alone.
	neutral bool
}

var writerPins = map[string]writerPin{
	// Every guarded move: transition checks the table and compare-and-sets on the phase.
	"transition": {moves: []phasePair{{PhaseQueued, PhaseSerialMergingMain}}},
	"removeRow":  {moves: []phasePair{{PhaseQueued, PhaseDequeued}, {PhaseSerialMerging, PhaseMerged}}},
	"EnqueueIn":  {enters: PhaseQueued},
	// A testing member released: testing -> released -> queued, as one write.
	"releaseMember": {moves: []phasePair{{PhaseBatchTesting, PhaseReleased}, {PhaseReleased, PhaseQueued}}},
	"dequeueSQL":    {moves: []phasePair{{PhaseQueued, PhaseDequeued}}},
	// Clears the reservation a row handed back by an older release still names.
	"Void": {neutral: true},
}

var landingsWrite = regexp.MustCompile(`(?i)\b(update\s+landings|insert\s+into\s+landings|delete\s+from\s+landings)\b`)

// writeSite is the SQL of one top-level declaration that writes landings.
type writeSite struct {
	sql string
	// assigns is the SQL up to each WHERE: the part that sets columns.
	assigns string
	// calls are the identifiers the declaration's body mentions.
	calls  map[string]bool
	isFunc bool
}

// landingsWriteSites finds, by declaration, every SQL string in the package's
// non-test source that writes the landings table.
func landingsWriteSites(t *testing.T) map[string]*writeSite {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	require.NoError(t, err)
	sites := map[string]*writeSite{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				var owner string
				switch d := decl.(type) {
				case *ast.FuncDecl:
					owner = d.Name.Name
				case *ast.GenDecl:
					if len(d.Specs) == 0 {
						continue
					}
					vs, ok := d.Specs[0].(*ast.ValueSpec)
					if !ok {
						continue
					}
					owner = vs.Names[0].Name
				}
				site := &writeSite{calls: map[string]bool{}}
				_, site.isFunc = decl.(*ast.FuncDecl)
				ast.Inspect(decl, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.BasicLit:
						if n.Kind == token.STRING {
							if lit, err := strconv.Unquote(n.Value); err == nil {
								site.sql += "\n" + lit
								before, _, _ := strings.Cut(strings.ToUpper(lit), "WHERE")
								site.assigns += "\n" + before
							}
						}
					case *ast.Ident:
						site.calls[n.Name] = true
					}
					return true
				})
				if landingsWrite.MatchString(site.sql) {
					sites[owner] = site
				}
			}
		}
	}
	return sites
}

// A write on landings that the state table does not account for fails here, so
// a new way to move an entry has to be a transition first.
func TestEveryLandingsWriteIsATableTransition(t *testing.T) {
	sites := landingsWriteSites(t)
	for owner, site := range sites {
		pin, ok := writerPins[owner]
		if !assert.True(t, ok, "%s writes landings and maps to no transition: add the transition to the table and pin it in writerPins", owner) {
			continue
		}
		setsPhase := strings.Contains(strings.ToLower(site.assigns), "queue_state")
		assert.Equal(t, !pin.neutral, setsPhase || pin.enters != "" || strings.Contains(strings.ToLower(site.sql), "delete from landings"),
			"%s: a write that sets the phase is not neutral, and a neutral one must not", owner)
		for _, m := range pin.moves {
			_, legal := Lookup(m.from, m.to)
			assert.True(t, legal, "%s pins %s, which the table does not have", owner, m)
		}
		if pin.enters != "" {
			assert.Contains(t, site.sql, "'"+string(pin.enters)+"'", "%s inserts a row that does not start %s", owner, pin.enters)
		}
		if site.isFunc && !pin.neutral && pin.enters == "" {
			assert.True(t, site.calls["checkMove"] || site.calls["Lookup"], "%s moves the phase without checking the table", owner)
		}
		if !site.isFunc {
			for _, m := range pin.moves {
				assert.Contains(t, site.sql, "'"+string(m.from)+"'", "%s is not guarded on %s", owner, m.from)
			}
		}
	}
	for owner := range writerPins {
		assert.Contains(t, sites, owner, "writerPins names %s, which no longer writes landings", owner)
	}
}
