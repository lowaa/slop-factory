// Package complexity is the distribution of cyclomatic (gocyclo) and
// cognitive (gocognit) complexity over a module's functions, and its longest
// functions, ratcheted against quality_baseline/complexity.json.
//
// lint gates each function over a threshold; this watches the whole: a
// module whose functions all creep up stays under lint's thresholds and
// still shows here.
package complexity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fzipp/gocyclo"
	"github.com/uudashr/gocognit"

	"github.com/lowaa/slop-factory/dev-tools/quality/internal/gosrc"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/ratchet"
)

const (
	// CyclomaticThreshold matches lint's oxlint `complexity` and ruff C901.
	CyclomaticThreshold = 15
	// CognitiveThreshold matches lint's gocognit.
	CognitiveThreshold = 20
	// LongestN is how many of the longest functions the baseline records.
	LongestN = 10
	// MeanTolerance is how far a mean may rise before it regresses. Removing
	// a simple function raises the mean as surely as adding a complex one
	// does: without it, deleting a duplicated helper failed the check. As
	// with coverage's, it can't accumulate: the baseline never rises.
	MeanTolerance = 0.1
)

// Function is one measured function.
type Function struct {
	File       string `json:"file"`
	Function   string `json:"function"`
	Lines      int    `json:"lines"`
	Cyclomatic int    `json:"cyclomatic"`
	Cognitive  int    `json:"cognitive"`
}

// Distribution summarises one complexity measure.
type Distribution struct {
	Threshold     int     `json:"threshold"`
	Mean          float64 `json:"mean"`
	P90           int     `json:"p90"`
	Max           int     `json:"max"`
	OverThreshold int     `json:"over_threshold"`
}

// Snapshot is complexity.json. Functions is the full list, in the report
// only; Longest is the top LongestN by lines, in both.
type Snapshot struct {
	Count      int          `json:"functions"`
	Cyclomatic Distribution `json:"cyclomatic"`
	Cognitive  Distribution `json:"cognitive"`
	Longest    []Function   `json:"longest"`
	Functions  []Function   `json:"all_functions,omitempty"`
}

// Metric is complexity as a ratchet.Metric.
type Metric struct {
	Module *gosrc.Module
}

var _ ratchet.Metric[*Snapshot] = (*Metric)(nil)

// Name implements ratchet.Metric.
func (m *Metric) Name() string { return "complexity" }

// File implements ratchet.Metric.
func (m *Metric) File() string { return "complexity.json" }

// Gated implements ratchet.Metric.
func (m *Metric) Gated() bool { return true }

// Measure walks the module's non-test, non-generated Go files, less
// testdata, vendor, hidden directories and nested modules (which measure
// themselves).
func (m *Metric) Measure() (*Snapshot, error) {
	dirs, err := m.sourceDirs()
	if err != nil {
		return nil, err
	}
	var fns []Function
	for _, dir := range dirs {
		got, err := m.measureDir(dir)
		if err != nil {
			return nil, err
		}
		fns = append(fns, got...)
	}
	return summarise(fns), nil
}

// sourceDirs maps each directory to its Go files, module-relative.
func (m *Metric) sourceDirs() ([][]string, error) {
	byDir := map[string][]string{}
	err := filepath.WalkDir(m.Module.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return m.skipDir(p, d)
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(m.Module.Dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		gen, err := m.Module.IsGenerated(rel)
		if err != nil || gen {
			return err
		}
		dir := filepath.Dir(rel)
		byDir[dir] = append(byDir[dir], rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(byDir))
	for k := range byDir {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, byDir[k])
	}
	return out, nil
}

func (m *Metric) skipDir(p string, d fs.DirEntry) error {
	if p == m.Module.Dir {
		return nil
	}
	name := d.Name()
	if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return filepath.SkipDir
	}
	if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
		return filepath.SkipDir
	}
	return nil
}

// measureDir measures one package's files together, so gocognit sees
// recursion across them.
func (m *Metric) measureDir(relFiles []string) ([]Function, error) {
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(relFiles))
	for _, rel := range relFiles {
		f, err := parser.ParseFile(fset, filepath.Join(m.Module.Dir, filepath.FromSlash(rel)), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	recursive := gocognit.RecursiveFuncs(files, nil)
	var fns []Function
	for i, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// The scan, not ComplexityStats: that skips functions marked
			// `gocognit:ignore`, which lint may excuse but the
			// distribution still counts.
			cog := gocognit.ScanComplexityWithRecursion(fn, recursive[fn], false).Complexity
			fns = append(fns, Function{
				File:       relFiles[i],
				Function:   funcName(fn),
				Lines:      fset.Position(fn.End()).Line - fset.Position(fn.Pos()).Line + 1,
				Cyclomatic: gocyclo.Complexity(fn),
				Cognitive:  cog,
			})
		}
	}
	return fns, nil
}

// funcName is "F", "T.M" or "(*T).M", as lint names functions.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return recvName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return "(*" + recvName(t.X) + ")"
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

func summarise(fns []Function) *Snapshot {
	sort.Slice(fns, func(i, j int) bool {
		if fns[i].Lines != fns[j].Lines {
			return fns[i].Lines > fns[j].Lines
		}
		if fns[i].File != fns[j].File {
			return fns[i].File < fns[j].File
		}
		return fns[i].Function < fns[j].Function
	})
	cyc := make([]int, len(fns))
	cog := make([]int, len(fns))
	for i, f := range fns {
		cyc[i], cog[i] = f.Cyclomatic, f.Cognitive
	}
	longest := append([]Function{}, fns[:min(LongestN, len(fns))]...)
	return &Snapshot{
		Count:      len(fns),
		Cyclomatic: distribution(cyc, CyclomaticThreshold),
		Cognitive:  distribution(cog, CognitiveThreshold),
		Longest:    longest,
		Functions:  fns,
	}
}

func distribution(values []int, threshold int) Distribution {
	d := Distribution{Threshold: threshold}
	if len(values) == 0 {
		return d
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	sum := 0
	for _, v := range sorted {
		sum += v
		if v > threshold {
			d.OverThreshold++
		}
	}
	d.Mean = math.Round(100*float64(sum)/float64(len(sorted))) / 100
	// Nearest rank: the smallest value at least 90% of functions are at
	// or below, always one that occurs.
	d.P90 = sorted[int(math.Ceil(0.9*float64(len(sorted))))-1]
	d.Max = sorted[len(sorted)-1]
	return d
}

// Compare implements ratchet.Metric: a mean rising more than
// MeanTolerance regresses, and so does any rise in a p90, max or
// over-threshold count. The function count and the longest list are
// recorded, not gated: a module may grow.
func (m *Metric) Compare(base, cur *Snapshot) *ratchet.Result {
	res := &ratchet.Result{}
	compareDist(res, "cyclomatic", &base.Cyclomatic, &cur.Cyclomatic)
	compareDist(res, "cognitive", &base.Cognitive, &cur.Cognitive)
	return res
}

func compareDist(res *ratchet.Result, name string, base, cur *Distribution) {
	compareValue(res, name+" mean", base.Mean, cur.Mean, MeanTolerance, "%.2f")
	compareValue(res, name+" p90", float64(base.P90), float64(cur.P90), 0, "%.0f")
	compareValue(res, name+" max", float64(base.Max), float64(cur.Max), 0, "%.0f")
	compareValue(res, fmt.Sprintf("%s functions over %d", name, cur.Threshold),
		float64(base.OverThreshold), float64(cur.OverThreshold), 0, "%.0f")
}

// compareValue: lower is better. Means are stored to two decimals, so
// half a hundredth separates a real change from float noise.
func compareValue(res *ratchet.Result, key string, base, cur, tolerance float64, format string) {
	msg := fmt.Sprintf("%s: "+format+", baseline "+format, key, cur, base)
	switch {
	case cur > base+tolerance+0.005:
		res.Regressions = append(res.Regressions, msg)
	case cur < base-0.005:
		res.Improvements = append(res.Improvements, msg)
	}
}

// Prune implements ratchet.Metric: each gated value falls to the current
// one where it is lower; the recorded ones take the current values.
func (m *Metric) Prune(base, cur *Snapshot) *Snapshot {
	return &Snapshot{
		Count:      cur.Count,
		Cyclomatic: pruneDist(&base.Cyclomatic, &cur.Cyclomatic),
		Cognitive:  pruneDist(&base.Cognitive, &cur.Cognitive),
		Longest:    cur.Longest,
	}
}

func pruneDist(base, cur *Distribution) Distribution {
	return Distribution{
		Threshold:     cur.Threshold,
		Mean:          math.Min(base.Mean, cur.Mean),
		P90:           min(base.P90, cur.P90),
		Max:           min(base.Max, cur.Max),
		OverThreshold: min(base.OverThreshold, cur.OverThreshold),
	}
}

// Encode implements ratchet.Metric: the baseline, without the full list.
func (m *Metric) Encode(s *Snapshot) ([]byte, error) {
	c := *s
	c.Functions = nil
	return ratchet.EncodeJSON(&c)
}

// Decode implements ratchet.Metric.
func (m *Metric) Decode(data []byte) (*Snapshot, error) {
	var s Snapshot
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Report implements ratchet.Metric: the baseline plus every function, by
// length.
func (m *Metric) Report(cur *Snapshot) ([]byte, error) { return ratchet.EncodeJSON(cur) }
