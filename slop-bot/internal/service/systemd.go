package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// unitName is the systemd user unit.
const unitName = "slop-bot.service"

// systemd is the Linux Manager: a user unit driven by `systemctl --user`.
type systemd struct {
	set *settings
	cmd *commander
}

// UnitPath is $XDG_CONFIG_HOME/systemd/user/slop-bot.service, using the
// resolved config base, which is one of systemd's user unit directories.
func (s *systemd) UnitPath() string {
	return filepath.Join(filepath.Dir(s.set.Config.Path), "systemd", "user", unitName)
}

func (s *systemd) Install(ctx context.Context) (*Installed, error) {
	sp, err := newSpec(s.set, false)
	if err != nil {
		return nil, err
	}
	// systemd refuses these in an executable path however they're quoted.
	if strings.ContainsAny(sp.Binary, `"'\`) {
		return nil, fmt.Errorf("service: systemd can't run %s: its path has a quote or backslash; "+
			"install slop-bot somewhere without one, or pass --binary", sp.Binary)
	}
	// The unit doesn't write XDG_RUNTIME_DIR, so the daemon gets logind's
	// /run/user/<uid>. A CLI resolving somewhere else would never find the
	// socket.
	want := filepath.Join("/run/user", strconv.Itoa(s.set.UID), "slop-bot")
	if got := s.set.Runtime.Path; got != want {
		sp.Warnings = append(sp.Warnings, fmt.Sprintf(
			"your runtime dir is %s, but the service's will be %s: the CLI won't find the daemon's socket "+
				"until XDG_RUNTIME_DIR is /run/user/%d in your shell", got, want, s.set.UID))
	}
	unit, err := render("slop-bot.service.tmpl", sp)
	if err != nil {
		return nil, err
	}
	if err := s.checkManagerReadsUnitDir(ctx); err != nil {
		return nil, err
	}
	if err := writeFile(s.UnitPath(), unit); err != nil {
		return nil, fmt.Errorf("service: writing %s: %w", s.UnitPath(), err)
	}
	// enable, then restart rather than `enable --now`: --now leaves an
	// already-running daemon on the old unit, and restart also starts a
	// stopped one.
	for _, args := range [][]string{{"daemon-reload"}, {"enable", unitName}, {"restart", unitName}} {
		if _, err := s.systemctl(ctx, args...); err != nil {
			return nil, err
		}
	}
	res := sp.installed(s.UnitPath())
	res.Linger = s.linger(ctx)
	return res, nil
}

// checkManagerReadsUnitDir refuses to write a unit the user manager won't
// find. The manager looks under its own $XDG_CONFIG_HOME, not the shell's:
// a variable exported only in a shell profile would put the unit where
// `enable` can't see it, and `uninstall` couldn't disable it.
func (s *systemd) checkManagerReadsUnitDir(ctx context.Context) error {
	res, err := s.systemctl(ctx, "show-environment")
	if err != nil {
		return err
	}
	managers := filepath.Join(s.set.Home, ".config")
	for _, line := range strings.Split(res.Stdout, "\n") {
		if v, ok := strings.CutPrefix(line, "XDG_CONFIG_HOME="); ok && filepath.IsAbs(v) {
			managers = filepath.Clean(v)
		}
	}
	if ours := filepath.Dir(s.set.Config.Path); ours != managers {
		return fmt.Errorf("service: your XDG_CONFIG_HOME is %s but systemd's user manager reads units from "+
			"%s/systemd/user; give the manager the same value (`systemctl --user import-environment "+
			"XDG_CONFIG_HOME`, or a file in ~/.config/environment.d), or unset it in your shell", ours, managers)
	}
	return nil
}

func (s *systemd) Uninstall(ctx context.Context) error {
	if err := s.requireInstalled(); err != nil {
		return err
	}
	if _, err := s.systemctl(ctx, "disable", "--now", unitName); err != nil {
		return err
	}
	if err := os.Remove(s.UnitPath()); err != nil {
		return fmt.Errorf("service: removing %s: %w", s.UnitPath(), err)
	}
	_, err := s.systemctl(ctx, "daemon-reload")
	return err
}

func (s *systemd) Start(ctx context.Context) error {
	if err := s.requireInstalled(); err != nil {
		return err
	}
	_, err := s.systemctl(ctx, "start", unitName)
	return err
}

func (s *systemd) Stop(ctx context.Context) error {
	if err := s.requireInstalled(); err != nil {
		return err
	}
	_, err := s.systemctl(ctx, "stop", unitName)
	return err
}

func (s *systemd) Status(ctx context.Context) (*Status, error) {
	st := &Status{UnitPath: s.UnitPath(), Linger: s.linger(ctx)}
	var err error
	if st.Installed, err = fileExists(s.UnitPath()); err != nil {
		return nil, err
	}
	if !st.Installed {
		st.State = "not installed"
		return st, nil
	}
	// is-active and is-enabled answer through the exit code, and print the
	// state either way; no output at all means systemctl itself failed
	// (no user bus, say).
	active, err := s.query(ctx, "is-active", unitName)
	if err != nil {
		return nil, err
	}
	st.Running, st.State = active.ExitCode == 0, strings.TrimSpace(active.Stdout)
	enabled, err := s.query(ctx, "is-enabled", unitName)
	if err != nil {
		return nil, err
	}
	st.Enabled = enabled.ExitCode == 0
	return st, nil
}

func (s *systemd) query(ctx context.Context, args ...string) (*Result, error) {
	res, err := s.cmd.run(ctx, "systemctl", append([]string{"--user"}, args...)...)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 && strings.TrimSpace(res.Stdout) == "" {
		return nil, fmt.Errorf("service: systemctl --user %s exited %d: %s",
			strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return res, nil
}

func (s *systemd) systemctl(ctx context.Context, args ...string) (*Result, error) {
	return s.cmd.must(ctx, "systemctl", append([]string{"--user"}, args...)...)
}

// linger asks logind whether the user's manager outlives their sessions. It
// asks by uid, which needs no $USER. It is reported, never switched on:
// enable-linger changes how the whole account behaves, so it's the user's
// call.
func (s *systemd) linger(ctx context.Context) Linger {
	res, err := s.cmd.must(ctx, "loginctl", "show-user", strconv.Itoa(s.set.UID), "-p", "Linger")
	if err != nil {
		s.set.Logger.Debug("linger check failed", "err", err)
		return LingerUnknown
	}
	switch strings.TrimSpace(res.Stdout) {
	case "Linger=yes":
		return LingerOn
	case "Linger=no":
		return LingerOff
	default:
		return LingerUnknown
	}
}

func (s *systemd) requireInstalled() error { return requireFile(s.UnitPath()) }

func requireFile(path string) error {
	ok, err := fileExists(path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w (no %s)", ErrNotInstalled, path)
	}
	return nil
}
