package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Label is the launchd job label, and the plist's file name. Reverse-DNS
// as launchd asks, so it can't collide with another vendor's agent; it
// names the project, not a domain anyone resolves.
const Label = "dev.slop-bot.daemon"

// launchd is the macOS Manager: a per-user agent in the gui/<uid> domain.
type launchd struct {
	set *settings
	cmd *commander
}

// UnitPath is ~/Library/LaunchAgents/<Label>.plist, where launchd loads
// the user's agents from at login.
func (l *launchd) UnitPath() string {
	return filepath.Join(l.set.Home, "Library", "LaunchAgents", Label+".plist")
}

func (l *launchd) domain() string { return "gui/" + strconv.Itoa(l.set.UID) }
func (l *launchd) target() string { return l.domain() + "/" + Label }

func (l *launchd) Install(ctx context.Context) (*Installed, error) {
	sp, err := newSpec(l.set, true)
	if err != nil {
		return nil, err
	}
	sp.Label = Label
	plist, err := render("slop-bot.plist.tmpl", sp)
	if err != nil {
		return nil, err
	}
	// launchd opens the log file but won't create its directory.
	if err := os.MkdirAll(filepath.Dir(sp.LogFile), 0o700); err != nil {
		return nil, err
	}
	// Written before the bootout, so a failed write leaves the old job
	// running.
	if err := writeFile(l.UnitPath(), plist); err != nil {
		return nil, fmt.Errorf("service: writing %s: %w", l.UnitPath(), err)
	}
	// A loaded job keeps the plist it was bootstrapped with, so a
	// reinstall boots the old one out first.
	if err := l.bootout(ctx); err != nil {
		return nil, err
	}
	// enable clears a disable left by an earlier `launchctl disable`,
	// which would otherwise make bootstrap refuse.
	if _, err := l.cmd.must(ctx, "launchctl", "enable", l.target()); err != nil {
		return nil, err
	}
	if _, err := l.cmd.must(ctx, "launchctl", "bootstrap", l.domain(), l.UnitPath()); err != nil {
		return nil, err
	}
	res := sp.installed(l.UnitPath())
	res.Linger = LingerNotApplicable
	return res, nil
}

// Preflight checks the user's GUI domain exists: launchd has one only while
// the user is logged in at the Mac, so a login over ssh alone can't
// bootstrap an agent.
func (l *launchd) Preflight(ctx context.Context) error {
	res, err := l.cmd.run(ctx, "launchctl", "print", l.domain())
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoServiceManager, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%w: launchctl print %s exited %d (launchd agents need you logged in at the Mac, "+
			"not only over ssh): %s", ErrNoServiceManager, l.domain(), res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (l *launchd) Uninstall(ctx context.Context) error {
	if err := requireFile(l.UnitPath()); err != nil {
		return err
	}
	if err := l.bootout(ctx); err != nil {
		return err
	}
	if err := os.Remove(l.UnitPath()); err != nil {
		return fmt.Errorf("service: removing %s: %w", l.UnitPath(), err)
	}
	return nil
}

// Start loads the job, which starts it (RunAtLoad); a job already loaded
// is kickstarted, which starts it only if it isn't running.
func (l *launchd) Start(ctx context.Context) error {
	if err := requireFile(l.UnitPath()); err != nil {
		return err
	}
	loaded, err := l.print(ctx)
	if err != nil {
		return err
	}
	if loaded != nil {
		_, err = l.cmd.must(ctx, "launchctl", "kickstart", l.target())
		return err
	}
	_, err = l.cmd.must(ctx, "launchctl", "bootstrap", l.domain(), l.UnitPath())
	return err
}

// Stop unloads the job: with KeepAlive set, killing it would only have
// launchd start it again. The plist stays, so it loads again at next login,
// as a stopped systemd unit starts again at boot.
func (l *launchd) Stop(ctx context.Context) error {
	if err := requireFile(l.UnitPath()); err != nil {
		return err
	}
	return l.bootout(ctx)
}

// launchdState is the job's own state line. Only one tab deep: nested
// sections of `launchctl print` (endpoints, event triggers) have state
// lines of their own.
var launchdState = regexp.MustCompile(`(?m)^\tstate = (.+?)\s*$`)

func (l *launchd) Status(ctx context.Context) (*Status, error) {
	st := &Status{UnitPath: l.UnitPath(), Linger: LingerNotApplicable, State: "not loaded"}
	var err error
	if st.Installed, err = fileExists(l.UnitPath()); err != nil {
		return nil, err
	}
	printed, err := l.print(ctx)
	if err != nil {
		return nil, err
	}
	if printed != nil {
		st.State = "loaded"
		if m := launchdState.FindStringSubmatch(printed.Stdout); m != nil {
			st.State = m[1]
		}
		st.Running = st.State == "running"
	}
	disabled, err := l.disabled(ctx)
	if err != nil {
		return nil, err
	}
	// An installed plist loads at login unless the label is disabled.
	st.Enabled = st.Installed && !disabled
	return st, nil
}

// launchctlNoSuchService is the exit code `launchctl print` gives for a job
// that isn't loaded. Any other failure is an error, not "not loaded": Stop
// and Uninstall would otherwise report success over a running job.
const launchctlNoSuchService = 113

// print returns `launchctl print` for the job, or nil when it isn't loaded.
func (l *launchd) print(ctx context.Context) (*Result, error) {
	res, err := l.cmd.run(ctx, "launchctl", "print", l.target())
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 0:
		return res, nil
	case launchctlNoSuchService:
		return nil, nil
	default:
		return nil, fmt.Errorf("service: launchctl print %s exited %d: %s",
			l.target(), res.ExitCode, strings.TrimSpace(res.Stderr))
	}
}

func (l *launchd) bootout(ctx context.Context) error {
	loaded, err := l.print(ctx)
	if err != nil || loaded == nil {
		return err
	}
	_, err = l.cmd.must(ctx, "launchctl", "bootout", l.target())
	return err
}

// disabled reads the domain's disabled list, whose lines look like
// `"dev.slop-bot.daemon" => disabled` (or `=> true` on older macOS).
func (l *launchd) disabled(ctx context.Context) (bool, error) {
	res, err := l.cmd.must(ctx, "launchctl", "print-disabled", l.domain())
	if err != nil {
		return false, err
	}
	prefix := strconv.Quote(Label) + " =>"
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, prefix); ok {
			v = strings.TrimSpace(v)
			return v == "disabled" || v == "true", nil
		}
	}
	return false, nil
}
