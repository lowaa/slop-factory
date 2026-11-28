// Command mutation turns the gremlins reports mutation.sh collected into a
// summary, and ratchets each package's score against the project's
// mutation_baseline/score.txt.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lowaa/slop-factory/dev-tools/mutation/internal/gremlins"
	"github.com/lowaa/slop-factory/dev-tools/mutation/internal/score"
)

// Exit codes, as quality.sh's.
const (
	exitOK         = 0
	exitRegression = 1
	exitError      = 2
)

const baselineFile = "score.txt"

// App is the command with its streams.
type App struct {
	Out io.Writer
	Err io.Writer
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

type summarizeArgs struct {
	project     string
	baselineDir string
	mode        string
	reportDir   string
	pruneCmd    string
	results     listFlag
}

// Run executes args (without the program name) and returns the exit code.
func (a *App) Run(args []string) int {
	if len(args) == 0 || args[0] != "summarize" {
		_, _ = fmt.Fprintln(a.Err, "usage: mutation summarize --project DIR --mode MODE --result PKG=FILE...")
		return exitError
	}
	m, err := a.parse(args[1:])
	if err != nil {
		_, _ = fmt.Fprintf(a.Err, "mutation: %v\n", err)
		return exitError
	}
	failed, err := a.summarize(m)
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(a.Err, "mutation: %v\n", err)
		return exitError
	case failed:
		return exitRegression
	}
	return exitOK
}

func (a *App) parse(args []string) (*summarizeArgs, error) {
	m := &summarizeArgs{}
	flags := flag.NewFlagSet("summarize", flag.ContinueOnError)
	flags.SetOutput(a.Err)
	flags.StringVar(&m.project, "project", "", "the project directory")
	flags.StringVar(&m.baselineDir, "baseline-dir", "", "default <project>/mutation_baseline")
	flags.StringVar(&m.mode, "mode", "check", "check, prune, rebaseline or report")
	flags.StringVar(&m.reportDir, "report-dir", "", "where report mode writes")
	flags.StringVar(&m.pruneCmd, "prune-cmd", "mutation.sh --prune", "named in the hint after an improvement")
	flags.Var(&m.results, "result", "PKG=FILE: a package and its gremlins report; repeatable")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if m.project == "" || len(m.results) == 0 {
		return nil, errors.New("--project and at least one --result are required")
	}
	switch m.mode {
	case "check", "prune", "rebaseline":
	case "report":
		if m.reportDir == "" {
			return nil, errors.New("--mode report needs --report-dir")
		}
	default:
		return nil, fmt.Errorf("unknown mode %q", m.mode)
	}
	if m.baselineDir == "" {
		m.baselineDir = filepath.Join(m.project, "mutation_baseline")
	}
	return m, nil
}

func readRows(results []string) ([]*score.Row, error) {
	var rows []*score.Row
	for _, r := range results {
		pkg, file, ok := strings.Cut(r, "=")
		if !ok || pkg == "" || file == "" {
			return nil, fmt.Errorf("--result %q: want PKG=FILE", r)
		}
		whole, err := gremlins.ReadFile(file)
		if err != nil {
			return nil, err
		}
		rep := whole.Direct()
		rows = append(rows, &score.Row{Package: pkg, Counts: rep.Counts(), Survivors: rep.Survivors()})
	}
	return rows, nil
}

// summarize prints the summary, then applies the mode to the baseline.
func (a *App) summarize(m *summarizeArgs) (failed bool, err error) {
	rows, err := readRows(m.results)
	if err != nil {
		return false, err
	}
	score.WriteSummary(a.Out, rows)
	cur := score.Scores(rows)
	for _, r := range rows {
		if _, ok := cur[r.Package]; !ok {
			_, _ = fmt.Fprintf(a.Out, "note: %s decided no mutants (none covered by its tests), so it has no score\n", r.Package)
		}
	}
	gone := func(pkg string) bool {
		_, err := os.Stat(filepath.Join(m.project, filepath.FromSlash(pkg))) // #nosec G703 -- a path from the command line, the operator's own
		return errors.Is(err, fs.ErrNotExist)
	}
	path := filepath.Join(m.baselineDir, baselineFile)

	if m.reportDir != "" {
		if err := writeReport(m, rows); err != nil {
			return false, err
		}
		_, _ = fmt.Fprintf(a.Out, "report written to %s\n", m.reportDir)
	}
	if m.mode == "rebaseline" {
		base, _, err := a.readBaseline(path)
		if err != nil {
			return false, err
		}
		return false, a.writeBaseline(path, score.Rebaseline(base, cur, score.Undecided(rows), gone))
	}
	base, found, err := a.readBaseline(path)
	if err != nil {
		return false, err
	}
	if !found {
		if m.mode == "report" {
			_, _ = fmt.Fprintf(a.Out, "no baseline at %s, nothing to compare\n", path)
			return false, nil
		}
		return false, fmt.Errorf("no baseline at %s: create it with --rebaseline", path)
	}
	res := score.Compare(base, cur, gone)
	// A package that had a score and now has no covered mutants lost its
	// tests' reach: that is a drop, not an absence.
	for _, pkg := range score.Undecided(rows) {
		if _, had := base[pkg]; had {
			res.Regressions = append(res.Regressions, pkg+": no mutant is covered by its tests any more, but the baseline has a score")
		}
	}
	failed = a.printResult(m, res)
	if m.mode == "prune" {
		return failed, a.writeBaseline(path, score.Prune(base, cur, gone))
	}
	return failed, nil
}

func (a *App) printResult(m *summarizeArgs, res *score.Result) (failed bool) {
	label := "regressed"
	if m.mode == "report" {
		label = "regressed (report only)"
	}
	for _, s := range res.Regressions {
		_, _ = fmt.Fprintf(a.Out, "mutation %s: %s\n", label, s)
	}
	for _, s := range res.Improvements {
		_, _ = fmt.Fprintf(a.Out, "mutation improved: %s\n", s)
	}
	switch {
	case len(res.Regressions) > 0 && m.mode != "report":
		_, _ = fmt.Fprintln(a.Out, "mutation: the baseline only tightens; see dev-tools/mutation/README.md")
		return true
	case len(res.Improvements) > 0 && m.mode == "check":
		_, _ = fmt.Fprintf(a.Out, "mutation: lock the improvement in: %s\n", m.pruneCmd)
	case len(res.Regressions)+len(res.Improvements) == 0:
		_, _ = fmt.Fprintln(a.Out, "mutation: matches the baseline")
	}
	return false
}

func (a *App) readBaseline(path string) (score.Snapshot, bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the project's own baseline
	if errors.Is(err, fs.ErrNotExist) {
		return score.Snapshot{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	s, err := score.Decode(data)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return s, true, nil
}

// writeBaseline replaces the baseline file; it is committed, so an
// interrupted write is one revert away.
func (a *App) writeBaseline(path string, s score.Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { // #nosec G703 -- a path from the command line, the operator's own
		return err
	}
	// A committed baseline is an ordinary, non-secret file.
	if err := os.WriteFile(path, score.Encode(s), 0o644); err != nil { // #nosec G306,G703 -- a committed, non-secret file
		return err
	}
	_, _ = fmt.Fprintf(a.Out, "mutation: wrote %s\n", path)
	return nil
}

// writeReport writes what a person reads after a run: the summary, every
// survivor, and each package's raw gremlins report.
func writeReport(m *summarizeArgs, rows []*score.Row) error {
	if err := os.MkdirAll(filepath.Join(m.reportDir, "gremlins"), 0o750); err != nil { // #nosec G703 -- a path from the command line, the operator's own
		return err
	}
	var summary, survivors strings.Builder
	score.WriteSummary(&summary, rows)
	score.WriteSurvivors(&survivors, rows)
	files := map[string]string{"summary.txt": summary.String(), "survivors.txt": survivors.String()}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(m.reportDir, name), []byte(content), 0o600); err != nil { // #nosec G703 -- a path from the command line, the operator's own
			return err
		}
	}
	for _, r := range m.results {
		pkg, file, _ := strings.Cut(r, "=")
		data, err := os.ReadFile(file) // #nosec G304,G703 -- readRows read it already
		if err != nil {
			return err
		}
		name := reportName(pkg)
		if err := os.WriteFile(filepath.Join(m.reportDir, "gremlins", name), data, 0o600); err != nil { // #nosec G703 -- a path from the command line, the operator's own
			return err
		}
	}
	return nil
}

// reportName is the raw report's file name for a package: not hidden (the
// root package is "."), and one name per package (a/b and a-b differ).
func reportName(pkg string) string {
	pkg = strings.TrimPrefix(pkg, "./")
	if pkg == "" || pkg == "." {
		pkg = "root"
	}
	return strings.ReplaceAll(pkg, "/", "__") + ".json"
}
