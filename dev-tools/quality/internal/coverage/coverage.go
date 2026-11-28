// Package coverage is statement coverage per package and in total, from Go
// cover profiles and GOCOVERDIR directories, ratcheted against
// quality_baseline/coverage.txt.
package coverage

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/cover"

	"github.com/lowaa/slop-factory/dev-tools/quality/internal/gosrc"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/ratchet"
)

const (
	// Tolerance absorbs a percentage's rounding: one statement more or less
	// in a big package moves it by less than this.
	Tolerance = 0.1
	// NewPackageFloor is what a package not yet in the baseline must reach.
	NewPackageFloor = 80.0
	totalKey        = "total"
	// eps compares one-decimal values without float noise.
	eps = 1e-9
)

// Snapshot is coverage percentages, rounded to one decimal. Unmeasured
// lists the packages that have source but are in no profile at all, which
// a baseline never holds: they tell a package that is gone from one a
// partial profile (a --coverprofile of some packages' tests) left out.
type Snapshot struct {
	Packages   map[string]float64
	Total      float64
	Unmeasured map[string]bool
}

// Converter turns a GOCOVERDIR into a text profile.
type Converter interface {
	TextFormat(coverDir, outFile string) error
}

// Metric is coverage as a ratchet.Metric.
type Metric struct {
	Module    *gosrc.Module
	Profiles  []string
	CoverDirs []string
	Converter Converter
	// TempDir holds the profiles converted from CoverDirs.
	TempDir string
}

var _ ratchet.Metric[*Snapshot] = (*Metric)(nil)

// Name implements ratchet.Metric.
func (m *Metric) Name() string { return "coverage" }

// File implements ratchet.Metric.
func (m *Metric) File() string { return "coverage.txt" }

// Gated implements ratchet.Metric.
func (m *Metric) Gated() bool { return true }

// Measure merges every profile and cover directory into one snapshot.
func (m *Metric) Measure() (*Snapshot, error) {
	profiles := append([]string(nil), m.Profiles...)
	for i, dir := range m.CoverDirs {
		out := filepath.Join(m.TempDir, fmt.Sprintf("coverdir-%d.out", i))
		if err := m.Converter.TextFormat(dir, out); err != nil {
			return nil, fmt.Errorf("converting %s: %w", dir, err)
		}
		profiles = append(profiles, out)
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no cover profile or GOCOVERDIR given")
	}
	blocks := newBlockSet()
	for _, p := range profiles {
		parsed, err := cover.ParseProfiles(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		for _, prof := range parsed {
			blocks.add(prof)
		}
	}
	return m.snapshot(blocks)
}

type counts struct{ covered, total int }

func (c *counts) add(b *block) {
	c.total += b.stmts
	if b.covered {
		c.covered += b.stmts
	}
}

// count totals the module's own, hand-written files' statements by package.
func (m *Metric) count(blocks *blockSet) (map[string]*counts, error) {
	perPkg := map[string]*counts{}
	for file, fileBlocks := range blocks.files {
		rel, ok := m.Module.RelFile(file)
		if !ok {
			continue
		}
		gen, err := m.Module.IsGenerated(rel)
		if err != nil || gen {
			if err != nil {
				return nil, err
			}
			continue
		}
		pkg := gosrc.PackageOf(rel)
		if perPkg[pkg] == nil {
			perPkg[pkg] = &counts{}
		}
		for _, b := range fileBlocks {
			perPkg[pkg].add(b)
		}
	}
	return perPkg, nil
}

func (m *Metric) snapshot(blocks *blockSet) (*Snapshot, error) {
	perPkg, err := m.count(blocks)
	if err != nil {
		return nil, err
	}
	unmeasured, err := m.unmeasured(func(pkg string) bool { return perPkg[pkg] != nil })
	if err != nil {
		return nil, err
	}
	s := &Snapshot{Packages: map[string]float64{}, Unmeasured: unmeasured}
	var all counts
	for pkg, c := range perPkg {
		all.covered += c.covered
		all.total += c.total
		// A package of only declarations has nothing to cover.
		if c.total > 0 {
			s.Packages[pkg] = percent(c.covered, c.total)
		}
	}
	if all.total > 0 {
		s.Total = percent(all.covered, all.total)
	}
	return s, nil
}

// unmeasured is the packages with source that no profile measured.
func (m *Metric) unmeasured(measured func(pkg string) bool) (map[string]bool, error) {
	src, err := m.Module.SourceFiles()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for dir := range src {
		if pkg := gosrc.Package(dir); !measured(pkg) {
			out[pkg] = true
		}
	}
	return out, nil
}

func percent(covered, total int) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(100*float64(covered)/float64(total), 'f', 1, 64), 64)
	return v
}

type block struct {
	stmts   int
	covered bool
}

type blockKey struct{ startLine, startCol, endLine, endCol int }

// blockSet merges profiles by covered-or-not, not by count, so profiles of
// different modes (set from an e2e binary, atomic from -race) combine.
type blockSet struct {
	files map[string]map[blockKey]*block
}

func newBlockSet() *blockSet { return &blockSet{files: map[string]map[blockKey]*block{}} }

func (s *blockSet) add(p *cover.Profile) {
	fb := s.files[p.FileName]
	if fb == nil {
		fb = map[blockKey]*block{}
		s.files[p.FileName] = fb
	}
	for _, b := range p.Blocks {
		k := blockKey{b.StartLine, b.StartCol, b.EndLine, b.EndCol}
		if got := fb[k]; got != nil {
			got.covered = got.covered || b.Count > 0
			continue
		}
		fb[k] = &block{stmts: b.NumStmt, covered: b.Count > 0}
	}
}

// Compare implements ratchet.Metric: a package or the total below its
// baseline by more than Tolerance regresses, and so does a new package below
// NewPackageFloor.
func (m *Metric) Compare(base, cur *Snapshot) *ratchet.Result {
	res := &ratchet.Result{}
	for _, pkg := range sortedKeys(cur.Packages) {
		v := cur.Packages[pkg]
		b, known := base.Packages[pkg]
		if !known {
			if v+eps < NewPackageFloor {
				res.Regressions = append(res.Regressions, fmt.Sprintf(
					"%s: %.1f%%, a new package, below the %.0f%% floor", pkg, v, NewPackageFloor))
			} else {
				res.Improvements = append(res.Improvements, fmt.Sprintf("%s: new package at %.1f%%", pkg, v))
			}
			continue
		}
		compareOne(res, pkg, b, v)
	}
	for _, pkg := range sortedKeys(base.Packages) {
		_, ok := cur.Packages[pkg]
		switch {
		case ok:
		case cur.Unmeasured[pkg]:
			res.Regressions = append(res.Regressions, fmt.Sprintf(
				"%s: in the source but in no cover profile: a profile of only some packages' tests?", pkg))
		default:
			res.Improvements = append(res.Improvements, fmt.Sprintf("%s: gone, its baseline entry is stale", pkg))
		}
	}
	compareOne(res, totalKey, base.Total, cur.Total)
	return res
}

func compareOne(res *ratchet.Result, key string, base, cur float64) {
	switch {
	case cur+Tolerance+eps < base:
		res.Regressions = append(res.Regressions, fmt.Sprintf(
			"%s: %.1f%%, baseline %.1f%% (tolerance %.1f)", key, cur, base, Tolerance))
	case cur > base+eps:
		res.Improvements = append(res.Improvements, fmt.Sprintf("%s: %.1f%%, baseline %.1f%%", key, cur, base))
	}
}

// Prune implements ratchet.Metric: each value rises to the current one,
// gone packages leave, and new packages that pass the floor join. An
// unmeasured package keeps its entry: it isn't gone. Nor does the total
// move then: a total of some packages is no bar for all of them.
func (m *Metric) Prune(base, cur *Snapshot) *Snapshot {
	out := &Snapshot{Packages: map[string]float64{}, Total: max(base.Total, cur.Total)}
	for pkg := range cur.Unmeasured {
		if b, known := base.Packages[pkg]; known {
			out.Packages[pkg] = b
			out.Total = base.Total
		}
	}
	for pkg, v := range cur.Packages {
		b, known := base.Packages[pkg]
		switch {
		case known:
			out.Packages[pkg] = max(b, v)
		case v+eps >= NewPackageFloor:
			out.Packages[pkg] = v
		}
	}
	return out
}

// Encode implements ratchet.Metric: sorted lines, total last, so a diff of
// the baseline reads package by package.
func (m *Metric) Encode(s *Snapshot) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("# Statement coverage: <package> <percent>, then the total. Written by\n")
	buf.WriteString("# quality.sh; see dev-tools/quality/README.md.\n")
	for _, pkg := range sortedKeys(s.Packages) {
		fmt.Fprintf(&buf, "%s %.1f\n", pkg, s.Packages[pkg])
	}
	fmt.Fprintf(&buf, "%s %.1f\n", totalKey, s.Total)
	return buf.Bytes(), nil
}

// Decode implements ratchet.Metric.
func (m *Metric) Decode(data []byte) (*Snapshot, error) {
	s := &Snapshot{Packages: map[string]float64{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	haveTotal := false
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("line %d: want '<package> <percent>', got %q", n, line)
		}
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if fields[0] == totalKey {
			s.Total, haveTotal = v, true
			continue
		}
		s.Packages[fields[0]] = v
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !haveTotal {
		return nil, fmt.Errorf("no %q line", totalKey)
	}
	return s, nil
}

// Report implements ratchet.Metric: the baseline's own format, which
// already lists every package.
func (m *Metric) Report(cur *Snapshot) ([]byte, error) { return m.Encode(cur) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// GoConverter runs `go tool covdata textfmt`.
type GoConverter struct {
	// Go is the go command; empty means "go" on PATH.
	Go string
}

// TextFormat implements Converter.
func (g *GoConverter) TextFormat(coverDir, outFile string) error {
	goCmd := g.Go
	if goCmd == "" {
		goCmd = "go"
	}
	cmd := exec.Command(goCmd, "tool", "covdata", "textfmt", "-i="+coverDir, "-o="+outFile) // #nosec G204 -- the go tool, with paths the caller gives
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go tool covdata textfmt: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}
