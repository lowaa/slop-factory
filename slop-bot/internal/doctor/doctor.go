// Package doctor checks a machine can run slop-bot: the tools it needs, each
// configured harness (installed, a tested version, signed in, its model
// usable), the repos and workspace, the service manager, and that this
// shell and the installed service agree on where things are. It changes
// nothing and spends no model tokens; `slop-bot install` runs it first and
// refuses on a failure.
//
// Each harness's sign-in and model checks are the cheapest reliable ones
// found (probed against the versions in the tested table):
//
//	harness      signed in                                  model
//	claude-code  `claude auth status` (JSON, local state)   form only: no list without a request
//	codex        `codex login status` (exit code)           app-server `model/list`
//	pi           `pi auth check --model M --no-refresh`     `pi --list-models`
//	agy          `agy models` (a list needs a login)        the same list
package doctor

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

// Level is a check's outcome.
type Level int

const (
	// Pass is fine.
	Pass Level = iota
	// Skip is a check that doesn't apply here (tmux with no TUI driver).
	Skip
	// Warn is worth knowing but doesn't stop slop-bot running.
	Warn
	// Fail stops slop-bot working: install refuses, doctor exits non-zero.
	Fail
)

func (l Level) String() string {
	switch l {
	case Pass:
		return "ok"
	case Skip:
		return "skip"
	case Warn:
		return "warn"
	default:
		return "FAIL"
	}
}

// Result is one check's outcome.
type Result struct {
	// Group heads the report section: "config", "harness pi", ...
	Group  string
	Name   string
	Level  Level
	Detail string
	// Fix says what to do about a warning or failure, for this OS.
	Fix string
}

// Report is every check, in the order run.
type Report struct {
	Results []Result
}

// Failed reports whether any check failed.
func (r *Report) Failed() bool {
	return slices.ContainsFunc(r.Results, func(res Result) bool { return res.Level == Fail })
}

func (r *Report) count(l Level) int {
	n := 0
	for _, res := range r.Results {
		if res.Level == l {
			n++
		}
	}
	return n
}

// Write prints the report: a section per group, a fix under each warning
// and failure, and a closing tally.
func (r *Report) Write(w io.Writer) error {
	var b strings.Builder
	group := ""
	for _, res := range r.Results {
		if res.Group != group {
			if group != "" {
				b.WriteString("\n")
			}
			group = res.Group
			b.WriteString(group + "\n")
		}
		fmt.Fprintf(&b, "  %-4s  %-16s %s\n", res.Level, res.Name, res.Detail)
		if res.Fix != "" && res.Level >= Warn {
			fmt.Fprintf(&b, "        %-16s fix: %s\n", "", res.Fix)
		}
	}
	fails, warns := r.count(Fail), r.count(Warn)
	b.WriteString("\n")
	switch {
	case fails > 0:
		fmt.Fprintf(&b, "%s, %s: slop-bot can't run until the failures are fixed\n",
			plural(fails, "failure"), plural(warns, "warning"))
	case warns > 0:
		fmt.Fprintf(&b, "no failures, %s\n", plural(warns, "warning"))
	default:
		b.WriteString("all checks passed\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Options are what the checks look at. Everything outside the process comes
// through them, so tests run every check against fakes and a temp home.
type Options struct {
	GOOS string
	// Lookup reads the environment the CLI runs in: API keys, for the
	// harnesses that may use one.
	Lookup func(string) (string, bool)
	// Environ is the environment harness probes start from (os.Environ),
	// before the API keys their agents wouldn't get are removed.
	Environ func() []string
	// Paths is the CLI's resolution; nil with PathsErr when it failed.
	Paths    *paths.Paths
	PathsErr error
	// ConfigFile is the config path checked. ConfigMissing means there is
	// no file there and Config holds the defaults; ConfigErr means it
	// didn't load.
	ConfigFile    string
	Config        *config.Config
	ConfigMissing bool
	ConfigErr     error
	// Service is nil when the OS has no service manager.
	Service  service.Manager
	Commands Commands
	// Installing is set by `slop-bot install`, which is about to write the
	// default briefing and (re)install the service: neither is missing yet.
	Installing bool
	// Implemented reports whether this build runs a harness under a
	// driver; nil is daemon.Implemented. Tests stand in a harness still to
	// come (agy's tui driver).
	Implemented func(name, driver string) bool
}

func (d *Doctor) implemented(name, driver string) bool {
	if d.opts.Implemented != nil {
		return d.opts.Implemented(name, driver)
	}
	return daemon.Implemented(name, driver)
}

// Doctor runs the checks.
type Doctor struct {
	opts   *Options
	report *Report
}

// New returns a Doctor over opts.
func New(opts *Options) *Doctor { return &Doctor{opts: opts} }

// Run runs every check in order. Checks never stop early: the report should
// show everything that's wrong at once.
func (d *Doctor) Run(ctx context.Context) *Report {
	d.report = &Report{}
	d.checkConfig()
	d.checkPrerequisites(ctx)
	if cfg := d.config(); cfg != nil {
		names := make([]string, 0, len(cfg.Harnesses))
		for name := range cfg.Harnesses {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			d.checkHarness(ctx, name, cfg.Harnesses[name])
		}
		d.checkRepos(ctx, cfg)
	}
	d.checkPlatform(ctx)
	return d.report
}

// config is the config the other checks use: nil when it didn't load, so
// they don't report against defaults the user didn't choose.
func (d *Doctor) config() *config.Config {
	if d.opts.ConfigErr != nil {
		return nil
	}
	return d.opts.Config
}

func (d *Doctor) add(group, name string, level Level, detail string, fix string) {
	d.report.Results = append(d.report.Results, Result{Group: group, Name: name, Level: level, Detail: detail, Fix: fix})
}

func (d *Doctor) env(name string) bool {
	v, ok := d.opts.Lookup(name)
	return ok && v != ""
}

// run runs a probe found on PATH; the fake keys on the binary's name.
func (d *Doctor) run(ctx context.Context, path string, args ...string) (*service.Result, error) {
	return d.opts.Commands.Run(ctx, &Command{Path: path, Args: args})
}

// hint is a fix that differs by OS.
type hint struct{ linux, darwin string }

func (d *Doctor) forOS(h hint) string {
	if d.opts.GOOS == "darwin" {
		return h.darwin
	}
	return h.linux
}

// pkg is the usual package-manager install for a tool.
func pkg(name string) hint {
	return hint{linux: "sudo apt install " + name + " (or your distribution's package manager)",
		darwin: "brew install " + name}
}

// firstLine trims a command's output to its first line, for a report row.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
