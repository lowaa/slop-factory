// Package testtime is a suite's wall time and slowest tests, from
// `go test -json`. It is report-only: timing is noisy, and the repo's tests
// may not depend on it, so a slower suite warns and never fails.
package testtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/dev-tools/quality/internal/gosrc"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/ratchet"
)

const (
	// WarnRatio is how far over the baseline's wall time a run warns.
	WarnRatio = 1.5
	// WarnMinSeconds is the least rise that warns: a suite of a second
	// doubles on a busy machine, and that says nothing about its tests.
	WarnMinSeconds = 1.0
	// SlowestN is how many of the slowest tests are recorded.
	SlowestN = 10
)

// Test is one top-level test's time.
type Test struct {
	Package string  `json:"package"`
	Test    string  `json:"test"`
	Seconds float64 `json:"seconds"`
}

// Snapshot is testtime.json.
type Snapshot struct {
	WallSeconds float64 `json:"wall_seconds"`
	Slowest     []Test  `json:"slowest"`
	All         []Test  `json:"all_tests,omitempty"`
}

// Event is one line of `go test -json` (cmd/test2json).
type Event struct {
	Time       time.Time `json:"Time"`
	Action     string    `json:"Action"`
	Package    string    `json:"Package"`
	ImportPath string    `json:"ImportPath"`
	Test       string    `json:"Test"`
	Elapsed    float64   `json:"Elapsed"`
	Output     string    `json:"Output"`
}

// ReadEvents reads a `go test -json` stream. A line that isn't an event
// (a tool's own stderr, folded in) is skipped.
func ReadEvents(r io.Reader) ([]Event, error) {
	var events []Event
	sc := bufio.NewScanner(r)
	// One test's output line can be long: a log of a large value.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	return events, sc.Err()
}

// ReadFile reads a `go test -json` file.
func ReadFile(p string) ([]Event, error) {
	f, err := os.Open(p) // #nosec G304 -- the go test output the caller names
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ReadEvents(f)
}

// Metric is test time as a ratchet.Metric.
type Metric struct {
	Module   *gosrc.Module
	TestJSON string
}

var _ ratchet.Metric[*Snapshot] = (*Metric)(nil)

// Name implements ratchet.Metric.
func (m *Metric) Name() string { return "testtime" }

// File implements ratchet.Metric.
func (m *Metric) File() string { return "testtime.json" }

// Gated implements ratchet.Metric.
func (m *Metric) Gated() bool { return false }

// Measure implements ratchet.Metric. The wall time is from the first event
// to the last: packages run in parallel, so their elapsed times don't add.
func (m *Metric) Measure() (*Snapshot, error) {
	events, err := ReadFile(m.TestJSON)
	if err != nil {
		return nil, err
	}
	s := &Snapshot{WallSeconds: wallSeconds(events)}
	for _, e := range events {
		// A subtest's time is in its parent's.
		if e.Test == "" || strings.Contains(e.Test, "/") || (e.Action != "pass" && e.Action != "fail") {
			continue
		}
		pkg, ok := m.Module.PackageOfImport(e.Package)
		if !ok {
			pkg = e.Package
		}
		s.All = append(s.All, Test{Package: pkg, Test: e.Test, Seconds: round2(e.Elapsed)})
	}
	sort.Slice(s.All, func(i, j int) bool {
		a, b := s.All[i], s.All[j]
		if a.Seconds != b.Seconds {
			return a.Seconds > b.Seconds
		}
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		return a.Test < b.Test
	})
	s.Slowest = append([]Test{}, s.All[:min(SlowestN, len(s.All))]...)
	return s, nil
}

func wallSeconds(events []Event) float64 {
	var first, last time.Time
	for _, e := range events {
		if e.Time.IsZero() {
			continue
		}
		if first.IsZero() || e.Time.Before(first) {
			first = e.Time
		}
		if e.Time.After(last) {
			last = e.Time
		}
	}
	return round2(last.Sub(first).Seconds())
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// Compare implements ratchet.Metric: only ever a warning.
func (m *Metric) Compare(base, cur *Snapshot) *ratchet.Result {
	res := &ratchet.Result{}
	if base.WallSeconds > 0 && cur.WallSeconds > base.WallSeconds*WarnRatio && cur.WallSeconds-base.WallSeconds > WarnMinSeconds {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"wall time %.2fs is over %.0f%% above the baseline %.2fs: look for a test that got slow (report-only, never fails)",
			cur.WallSeconds, 100*(WarnRatio-1), base.WallSeconds))
	}
	return res
}

// Prune implements ratchet.Metric: test time isn't ratcheted, so a fast run
// doesn't become the bar a normal one is warned against. Only --rebaseline
// moves it.
func (m *Metric) Prune(base, _ *Snapshot) *Snapshot { return base }

// Encode implements ratchet.Metric.
func (m *Metric) Encode(s *Snapshot) ([]byte, error) {
	c := *s
	c.All = nil
	return ratchet.EncodeJSON(&c)
}

// Decode implements ratchet.Metric.
func (m *Metric) Decode(data []byte) (*Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Report implements ratchet.Metric: every top-level test, slowest first.
func (m *Metric) Report(cur *Snapshot) ([]byte, error) { return ratchet.EncodeJSON(cur) }

// PrintFailures writes the output of every failed test and package, and
// every build failure, from a `go test -json` stream: what plain `go test`
// would have shown for them. A test that never finished (a timeout, a
// panic in a goroutine, os.Exit) has no fail event of its own, only its
// package does, so a failed package also shows its unfinished tests.
func PrintFailures(events []Event, w io.Writer) {
	p := &failurePrinter{w: w, output: map[testKey][]string{}, running: map[string][]string{}}
	for i := range events {
		p.event(&events[i])
	}
}

type testKey struct{ pkg, test string }

type failurePrinter struct {
	w      io.Writer
	output map[testKey][]string
	// running is each package's started tests, in order, until they end.
	running map[string][]string
}

func (p *failurePrinter) event(e *Event) {
	k := testKey{e.Package, e.Test}
	switch e.Action {
	case "run":
		p.running[e.Package] = append(p.running[e.Package], e.Test)
	case "output":
		p.output[k] = append(p.output[k], e.Output)
	case "build-output":
		_, _ = io.WriteString(p.w, e.Output)
	case "pass", "skip":
		p.end(k, false)
	case "fail":
		if e.Test == "" {
			for _, t := range p.running[e.Package] {
				p.flush(testKey{e.Package, t})
			}
			delete(p.running, e.Package)
		}
		p.end(k, true)
	}
}

func (p *failurePrinter) end(k testKey, show bool) {
	if show {
		p.flush(k)
	}
	delete(p.output, k)
	if k.test == "" {
		return
	}
	running := p.running[k.pkg]
	for i, t := range running {
		if t == k.test {
			p.running[k.pkg] = append(running[:i:i], running[i+1:]...)
			break
		}
	}
}

func (p *failurePrinter) flush(k testKey) {
	for _, line := range p.output[k] {
		_, _ = io.WriteString(p.w, line)
	}
	delete(p.output, k)
}
