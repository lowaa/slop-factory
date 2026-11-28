package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/dev-tools/quality/internal/complexity"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/coverage"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/gosrc"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/ratchet"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/testtime"
)

// Exit codes: a regression is 1, as lint's; a usage or tool error is 2, so
// a caller can tell "worse" from "couldn't tell".
const (
	exitOK         = 0
	exitRegression = 1
	exitError      = 2
)

var allMeasures = []string{"coverage", "complexity", "testtime"}

const usage = `usage:
  quality measure --module DIR --mode check|prune|rebaseline|report [options]
  quality failures TEST_JSON

measure options:
  --only LIST          comma-separated, of: coverage,complexity,testtime (default all)
  --coverprofile FILE  a cover profile; repeat to merge several
  --coverdir DIR       a GOCOVERDIR, converted with go tool covdata; repeatable
  --rerun-profile FILE a cover profile of one of several runs of the same suite;
                       repeatable: a block counts only if every rerun covers it
  --test-json FILE     go test -json output, for testtime
  --baseline-dir DIR   default MODULE/quality_baseline
  --report-dir DIR     where --mode report writes
  --prune-cmd CMD      the command the improvement hint names
`

// App is the quality command.
type App struct {
	Out io.Writer
	Err io.Writer
	// Converter is coverage's GOCOVERDIR converter; nil is the go tool's.
	Converter coverage.Converter
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

type measureArgs struct {
	module, mode, only, testJSON, baselineDir, reportDir, pruneCmd string
	profiles, coverDirs, reruns                                    listFlag
}

// Run runs the command line args and returns the exit code.
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		_, _ = io.WriteString(a.Err, usage)
		return exitError
	}
	switch args[0] {
	case "measure":
		return a.measure(args[1:])
	case "failures":
		return a.failures(args[1:])
	}
	_, _ = io.WriteString(a.Err, usage)
	return exitError
}

func (a *App) failures(args []string) int {
	if len(args) != 1 {
		_, _ = io.WriteString(a.Err, usage)
		return exitError
	}
	events, err := testtime.ReadFile(args[0])
	if err != nil {
		return a.fail(err)
	}
	testtime.PrintFailures(events, a.Err)
	return exitOK
}

func (a *App) fail(err error) int {
	_, _ = fmt.Fprintf(a.Err, "quality: %v\n", err)
	return exitError
}

func (a *App) parseMeasure(args []string) (*measureArgs, error) {
	fs := flag.NewFlagSet("measure", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	m := &measureArgs{}
	fs.StringVar(&m.module, "module", "", "")
	fs.StringVar(&m.mode, "mode", "", "")
	fs.StringVar(&m.only, "only", strings.Join(allMeasures, ","), "")
	fs.StringVar(&m.testJSON, "test-json", "", "")
	fs.StringVar(&m.baselineDir, "baseline-dir", "", "")
	fs.StringVar(&m.reportDir, "report-dir", "", "")
	fs.StringVar(&m.pruneCmd, "prune-cmd", "quality.sh --prune", "")
	fs.Var(&m.profiles, "coverprofile", "")
	fs.Var(&m.coverDirs, "coverdir", "")
	fs.Var(&m.reruns, "rerun-profile", "")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	switch {
	case fs.NArg() > 0:
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case m.module == "":
		return nil, errors.New("--module is required")
	case m.mode == "report" && m.reportDir == "":
		return nil, errors.New("--mode report needs --report-dir")
	}
	for _, name := range strings.Split(m.only, ",") {
		if !slices.Contains(allMeasures, name) {
			return nil, fmt.Errorf("--only: unknown measure %q", name)
		}
	}
	if m.baselineDir == "" {
		m.baselineDir = filepath.Join(m.module, "quality_baseline")
	}
	return m, nil
}

func (a *App) measure(args []string) int {
	m, err := a.parseMeasure(args)
	if err != nil {
		_, _ = fmt.Fprintf(a.Err, "quality: %v\n%s", err, usage)
		return exitError
	}
	mode, err := ratchet.ParseMode(m.mode)
	if err != nil {
		return a.fail(err)
	}
	mod, err := gosrc.LoadModule(m.module)
	if err != nil {
		return a.fail(err)
	}
	tmp, err := os.MkdirTemp("", "quality-")
	if err != nil {
		return a.fail(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	r := &ratchet.Runner{Mode: mode, BaselineDir: m.baselineDir, ReportDir: m.reportDir, PruneCmd: m.pruneCmd, Out: a.Out}
	only := strings.Split(m.only, ",")
	code := exitOK
	for _, name := range allMeasures {
		if !slices.Contains(only, name) {
			continue
		}
		failed, err := a.runOne(r, name, m, mod, tmp)
		switch {
		case err != nil:
			_, _ = fmt.Fprintf(a.Err, "quality: %v\n", err)
			code = exitError
		case failed && code == exitOK:
			code = exitRegression
		}
	}
	return code
}

func (a *App) runOne(r *ratchet.Runner, name string, m *measureArgs, mod *gosrc.Module, tmp string) (bool, error) {
	switch name {
	case "coverage":
		conv := a.Converter
		if conv == nil {
			conv = &coverage.GoConverter{}
		}
		return ratchet.Run(r, &coverage.Metric{
			Module: mod, Profiles: m.profiles, CoverDirs: m.coverDirs, Reruns: m.reruns, Converter: conv, TempDir: tmp,
		})
	case "complexity":
		return ratchet.Run(r, &complexity.Metric{Module: mod})
	default:
		if m.testJSON == "" {
			_, _ = fmt.Fprintln(a.Out, "testtime: skipped, no go test -json output (the tests weren't run)")
			return false, nil
		}
		return ratchet.Run(r, &testtime.Metric{Module: mod, TestJSON: m.testJSON})
	}
}
