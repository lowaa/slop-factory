// Package ratchet runs one measure against its committed baseline, in the
// mode quality.sh was given. Each measure decides what a regression is; this
// decides what a mode does with it, so every measure behaves alike.
package ratchet

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Mode is what a run does with the comparison.
type Mode string

const (
	// Check fails on any regression.
	Check Mode = "check"
	// Prune tightens the baseline where the measure improved, never
	// loosening it, and still fails on a regression.
	Prune Mode = "prune"
	// Rebaseline rewrites the baseline from the current values.
	Rebaseline Mode = "rebaseline"
	// Report writes the full report and never fails.
	Report Mode = "report"
)

// ParseMode reads a --mode value.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case Check, Prune, Rebaseline, Report:
		return m, nil
	}
	return "", fmt.Errorf("unknown mode %q", s)
}

// Result is a comparison of current values with a baseline.
type Result struct {
	// Regressions fail a check.
	Regressions []string
	// Improvements pass, and --prune locks them in.
	Improvements []string
	// Warnings never fail: they are for measures too noisy to gate on.
	Warnings []string
}

// Metric is one measure with a baseline file of its own.
type Metric[S any] interface {
	Name() string
	// File is the baseline's and the report's file name.
	File() string
	// Gated is false for a report-only measure: a missing baseline is then
	// a note rather than an error.
	Gated() bool
	Measure() (S, error)
	Compare(base, cur S) *Result
	// Prune returns base tightened by cur's improvements; it never loosens.
	Prune(base, cur S) S
	Encode(s S) ([]byte, error)
	Decode(data []byte) (S, error)
	// Report is the full report for --report, more than the baseline holds.
	Report(cur S) ([]byte, error)
}

// Runner holds what every measure's run shares.
type Runner struct {
	Mode        Mode
	BaselineDir string
	ReportDir   string
	// PruneCmd is printed in the hint after an improvement.
	PruneCmd string
	Out      io.Writer
}

// ErrNoBaseline is a gated measure's missing baseline in check or prune.
var ErrNoBaseline = errors.New("no baseline")

// Run measures m and applies the runner's mode. failed is a regression a
// check must fail on; err is a measure or baseline that could not be read
// or written.
func Run[S any](r *Runner, m Metric[S]) (failed bool, err error) {
	cur, err := m.Measure()
	if err != nil {
		return false, fmt.Errorf("%s: %w", m.Name(), err)
	}
	switch r.Mode {
	case Rebaseline:
		return false, writeEncoded(r, m, cur)
	case Report:
		data, err := m.Report(cur)
		if err != nil {
			return false, fmt.Errorf("%s: %w", m.Name(), err)
		}
		if err := r.write(m.Name(), filepath.Join(r.ReportDir, m.File()), data); err != nil {
			return false, err
		}
	}
	base, found, err := readBaseline(r, m)
	if err != nil {
		return false, err
	}
	if !found {
		return false, r.missingBaseline(m.Name(), m.File(), m.Gated())
	}
	res := m.Compare(base, cur)
	r.print(m.Name(), res)
	failed = len(res.Regressions) > 0 && r.Mode != Report
	if r.Mode == Prune {
		return failed, writeEncoded(r, m, m.Prune(base, cur))
	}
	return failed, nil
}

func (r *Runner) missingBaseline(name, file string, gated bool) error {
	p := filepath.Join(r.BaselineDir, file)
	if !gated || r.Mode == Report {
		_, _ = fmt.Fprintf(r.Out, "%s: no baseline at %s, nothing to compare\n", name, p)
		return nil
	}
	return fmt.Errorf("%s: %w at %s: create it with --rebaseline", name, ErrNoBaseline, p)
}

func readBaseline[S any](r *Runner, m Metric[S]) (base S, found bool, err error) {
	p := filepath.Join(r.BaselineDir, m.File())
	data, err := os.ReadFile(p) // #nosec G304 -- the project's own baseline
	if errors.Is(err, fs.ErrNotExist) {
		return base, false, nil
	}
	if err != nil {
		return base, false, err
	}
	base, err = m.Decode(data)
	if err != nil {
		return base, false, fmt.Errorf("%s: %w", p, err)
	}
	return base, true, nil
}

func writeEncoded[S any](r *Runner, m Metric[S], s S) error {
	data, err := m.Encode(s)
	if err != nil {
		return fmt.Errorf("%s: %w", m.Name(), err)
	}
	return r.write(m.Name(), filepath.Join(r.BaselineDir, m.File()), data)
}

// write replaces p by renaming a sibling into place, so an interrupted run
// leaves the old file whole.
func (r *Runner) write(name, p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp makes it 0600; a committed baseline is an ordinary file.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { // #nosec G302 -- a committed, non-secret file
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.Out, "%s: wrote %s\n", name, p)
	return nil
}

func (r *Runner) print(name string, res *Result) {
	label := "regressed"
	if r.Mode == Report {
		label = "regressed (report only)"
	}
	for _, s := range res.Regressions {
		_, _ = fmt.Fprintf(r.Out, "%s %s: %s\n", name, label, s)
	}
	for _, s := range res.Warnings {
		_, _ = fmt.Fprintf(r.Out, "%s warning: %s\n", name, s)
	}
	for _, s := range res.Improvements {
		_, _ = fmt.Fprintf(r.Out, "%s improved: %s\n", name, s)
	}
	switch {
	case len(res.Regressions) > 0 && r.Mode != Report:
		_, _ = fmt.Fprintf(r.Out, "%s: the baseline only tightens; see dev-tools/quality/README.md\n", name)
	case len(res.Improvements) > 0 && r.Mode == Check:
		_, _ = fmt.Fprintf(r.Out, "%s: lock the improvement in: %s\n", name, r.PruneCmd)
	case len(res.Regressions)+len(res.Warnings)+len(res.Improvements) == 0:
		_, _ = fmt.Fprintf(r.Out, "%s: matches the baseline\n", name)
	}
}

// EncodeJSON is a JSON baseline or report: indented, newline-terminated, so
// its diff reads field by field.
func EncodeJSON(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
