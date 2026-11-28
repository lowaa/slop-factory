// Package service installs and controls the slop-bot daemon as a per-user
// service: a systemd user unit on Linux, a launchd agent on macOS.
//
// The daemon under a service manager doesn't see the user's shell profile,
// so the installer resolves every path at install time and writes it into
// the unit or plist explicitly; the daemon then resolves the same paths the
// CLI does.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
)

// ErrNotInstalled means there is no unit or plist to act on.
var ErrNotInstalled = errors.New("service: slop-bot service is not installed")

// ErrUnsupported means slop-bot has no service manager for this OS.
var ErrUnsupported = errors.New("service: no service manager for this OS")

// stopMargin is how much longer than the daemon's own drain the service
// manager waits before it kills: the drain is bounded, but closing stores
// and exiting after it takes a moment too.
const stopMargin = 15 * time.Second

// StopTimeout is how long the service manager waits after SIGTERM before it
// SIGKILLs what is left.
const StopTimeout = daemon.StopTimeout + stopMargin

// Manager installs, removes and controls the slop-bot service.
type Manager interface {
	// Install writes the unit or plist, then enables and (re)starts the
	// service, so a reinstall picks up the new environment.
	Install(ctx context.Context) (*Installed, error)
	// Uninstall stops and disables the service and removes its file.
	Uninstall(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Status(ctx context.Context) (*Status, error)
	// UnitPath is where the unit or plist lives.
	UnitPath() string
	// Preflight checks the service manager would take an install, without
	// changing anything: it answers, and it reads units from where Install
	// writes them. ErrNoServiceManager when it doesn't answer.
	Preflight(ctx context.Context) error
	// ServiceEnv is the environment the installed service's daemon
	// resolves its paths in, read back from the unit or plist.
	// ErrNotInstalled when there is no unit. ServicePaths resolves it.
	ServiceEnv() (*paths.Env, error)
}

// EnvVar is one variable written into the unit or plist.
type EnvVar struct {
	Name  string
	Value string
}

// Linger is whether systemd keeps the user's services running with nobody
// logged in.
type Linger string

const (
	// LingerOn means the service survives logout and runs from boot.
	LingerOn Linger = "on"
	// LingerOff means the service stops when the user's last session ends.
	LingerOff Linger = "off"
	// LingerUnknown means loginctl couldn't say.
	LingerUnknown Linger = "unknown"
	// LingerNotApplicable is macOS: launchd agents run while the user is
	// logged in to the GUI, and there is no linger to switch on.
	LingerNotApplicable Linger = "n/a"
)

// Installed is what Install wrote.
type Installed struct {
	UnitPath string
	Binary   string
	Args     []string
	Env      []EnvVar
	Linger   Linger
	// Warnings are things the user should know but that didn't stop the
	// install, e.g. PATH entries that were dropped.
	Warnings []string
}

// Status is the service's state.
type Status struct {
	UnitPath  string
	Installed bool
	Enabled   bool
	Running   bool
	// State is the service manager's own word for it ("active",
	// "failed", "running", ...), for the user.
	State  string
	Linger Linger
}

// Options are what a Manager needs.
type Options struct {
	Paths *paths.Paths
	Home  string
	UID   int
	// GOOS picks the implementation: "linux" or "darwin".
	GOOS string
	// Binary is the absolute path of the slop-bot binary to run.
	Binary string
	// PATH is the PATH at install time. The daemon and its agents need the
	// user's toolchains (go, node, gh, ~/.local/bin), which a service
	// manager's minimal PATH lacks.
	PATH string
	// ConfigFile, when set, is an absolute config path the daemon is run
	// with (`daemon --config`); otherwise it reads the default one.
	ConfigFile string
	Runner     Runner
	Logger     *slog.Logger
}

// New returns the Manager for opts.GOOS.
func New(opts *Options) (Manager, error) {
	if opts == nil || opts.Paths == nil || opts.Runner == nil || opts.Logger == nil || opts.Home == "" {
		return nil, errors.New("service: New needs Paths, Home, Runner and Logger")
	}
	p := opts.Paths
	set := &settings{
		Home: opts.Home, UID: opts.UID, Binary: opts.Binary, PATH: opts.PATH,
		ConfigFile: opts.ConfigFile, Logger: opts.Logger,
		Config: p.Config, Data: p.Data, State: p.State, Cache: p.Cache, Runtime: p.Runtime,
		LogDir: p.LogDir(),
	}
	cmd := &commander{runner: opts.Runner, logger: opts.Logger}
	switch opts.GOOS {
	case "linux":
		return &systemd{set: set, cmd: cmd}, nil
	case "darwin":
		return &launchd{set: set, cmd: cmd}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, opts.GOOS)
	}
}

// settings is what the managers keep from Options. It copies the resolved
// directories rather than holding *paths.Paths: a Manager is an interface
// value, and text/template's reflection would then make deadcode count
// every Paths method as used.
type settings struct {
	Home                                string
	UID                                 int
	Binary, PATH, ConfigFile            string
	Logger                              *slog.Logger
	Config, Data, State, Cache, Runtime paths.Dir
	LogDir                              string
}

// spec is what the unit and plist templates render.
type spec struct {
	Label  string
	Binary string
	// Args follow Binary: "daemon", and any flags.
	Args        []string
	Env         []EnvVar
	WorkingDir  string
	LogFile     string
	StopSeconds int
	Warnings    []string
}

// newSpec builds the render input. withRuntime adds XDG_RUNTIME_DIR, which
// launchd needs and systemd sets itself.
func newSpec(set *settings, withRuntime bool) (*spec, error) {
	if err := checkBinary(set.Binary); err != nil {
		return nil, err
	}
	args := []string{"daemon"}
	if set.ConfigFile != "" {
		if !filepath.IsAbs(set.ConfigFile) {
			return nil, fmt.Errorf("service: the config path must be absolute, got %q", set.ConfigFile)
		}
		args = append(args, "--config", set.ConfigFile)
	}
	dirs := []paths.Dir{set.Config, set.Data, set.State, set.Cache}
	if withRuntime {
		dirs = append(dirs, set.Runtime)
	}
	s := &spec{
		Binary:      set.Binary,
		Args:        args,
		WorkingDir:  set.Home,
		LogFile:     filepath.Join(set.LogDir, "daemon.log"),
		StopSeconds: int(StopTimeout / time.Second),
	}
	for _, d := range dirs {
		// Each Dir is <base>/slop-bot; the variable names the base.
		s.Env = append(s.Env, EnvVar{Name: d.Var, Value: filepath.Dir(d.Path)})
	}
	path, dropped := cleanPATH(set.PATH)
	if len(dropped) > 0 {
		s.Warnings = append(s.Warnings, fmt.Sprintf(
			"dropped PATH entries that aren't absolute (the service would resolve them against its working directory): %q",
			dropped))
	}
	if path == "" {
		s.Warnings = append(s.Warnings,
			"PATH is empty, so the service gets the service manager's default PATH and may not find your toolchains")
	} else {
		s.Env = append(s.Env, EnvVar{Name: "PATH", Value: path})
	}
	return s, nil
}

// checkBinary refuses a binary the service manager couldn't run, before
// anything replaces a working unit.
func checkBinary(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("service: the slop-bot binary must be an absolute path, got %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("service: the slop-bot binary: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("service: the slop-bot binary %s isn't an executable file", path)
	}
	return nil
}

// cleanPATH keeps absolute entries, in order, without repeats. Relative and
// empty entries are dropped: under a service they would resolve against
// the working directory, which is how a planted binary gets run.
func cleanPATH(v string) (clean string, dropped []string) {
	seen := map[string]bool{}
	var keep []string
	for _, e := range strings.Split(v, string(os.PathListSeparator)) {
		if !filepath.IsAbs(e) {
			if e != "" {
				dropped = append(dropped, e)
			}
			continue
		}
		e = filepath.Clean(e)
		if !seen[e] {
			seen[e] = true
			keep = append(keep, e)
		}
	}
	return strings.Join(keep, string(os.PathListSeparator)), dropped
}

// writeFile replaces path atomically, so a failed write never leaves a
// half-written unit for the service manager to load.
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (s *spec) installed(unitPath string) *Installed {
	return &Installed{UnitPath: unitPath, Binary: s.Binary, Args: s.Args, Env: s.Env, Warnings: s.Warnings}
}
