// Package paths resolves where slop-bot keeps its files, following the XDG
// Base Directory spec on Linux and macOS alike. The daemon and the CLI both
// resolve through it, so they can't disagree about where the socket is.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// App is the directory name slop-bot uses under each base directory.
const App = "slop-bot"

// maxSocketPath is the shortest sun_path limit slop-bot runs on: macOS
// allows 104 bytes including the terminating NUL (Linux allows 108).
const maxSocketPath = 103

// Env is the environment the resolver reads, so tests needn't touch the real
// one.
type Env struct {
	// Lookup returns an environment variable and whether it is set.
	Lookup func(string) (string, bool)
	// Home is the user's home directory.
	Home string
	// UID is the user's numeric id, for the /run/user fallback.
	UID int
	// GOOS is "linux" or "darwin".
	GOOS string
	// Stat checks a fallback runtime directory; os.Stat in production.
	Stat func(string) (os.FileInfo, error)
	// OwnedByUser reports whether info belongs to UID; set by the
	// platform-specific constructor.
	OwnedByUser func(os.FileInfo) bool
}

// Source says where a resolved base directory came from, for `slop-bot
// paths` and the install summary.
type Source string

const (
	// FromEnv means the XDG variable was set.
	FromEnv Source = "env"
	// FromDefault means the spec's default was used.
	FromDefault Source = "default"
	// FromFallback means XDG_RUNTIME_DIR was unset and slop-bot chose one.
	FromFallback Source = "fallback"
)

// Dir is a resolved base directory and where it came from.
type Dir struct {
	Path   string
	Source Source
	// Var is the XDG variable it was resolved from.
	Var string
}

// Paths is every location slop-bot uses.
type Paths struct {
	Config  Dir
	Data    Dir
	State   Dir
	Cache   Dir
	Runtime Dir
}

// Resolve works out every base directory from env.
func Resolve(env *Env) (*Paths, error) {
	if env.Home == "" {
		return nil, errors.New("paths: no home directory")
	}
	p := &Paths{
		Config: base(env, "XDG_CONFIG_HOME", ".config"),
		Data:   base(env, "XDG_DATA_HOME", filepath.Join(".local", "share")),
		State:  base(env, "XDG_STATE_HOME", filepath.Join(".local", "state")),
		Cache:  base(env, "XDG_CACHE_HOME", ".cache"),
	}
	rt, err := runtimeDir(env)
	if err != nil {
		return nil, err
	}
	p.Runtime = rt
	if n := len(p.Socket()); n > maxSocketPath {
		return nil, fmt.Errorf("paths: socket path %s is %d bytes, over the %d-byte limit; "+
			"set XDG_RUNTIME_DIR to a shorter directory", p.Socket(), n, maxSocketPath)
	}
	return p, nil
}

func base(env *Env, name, defaultRel string) Dir {
	// The spec says a relative value is invalid and must be ignored.
	if v, ok := env.Lookup(name); ok && filepath.IsAbs(v) {
		return Dir{Path: filepath.Join(v, App), Source: FromEnv, Var: name}
	}
	return Dir{Path: filepath.Join(env.Home, defaultRel, App), Source: FromDefault, Var: name}
}

func runtimeDir(env *Env) (Dir, error) {
	const name = "XDG_RUNTIME_DIR"
	if v, ok := env.Lookup(name); ok && filepath.IsAbs(v) {
		return Dir{Path: filepath.Join(v, App), Source: FromEnv, Var: name}, nil
	}
	switch env.GOOS {
	case "darwin":
		// Nothing sets XDG_RUNTIME_DIR on macOS. $TMPDIR is already
		// per-user and cleared across reboots, the lifetime the spec asks
		// for.
		if tmp, ok := env.Lookup("TMPDIR"); ok && filepath.IsAbs(tmp) {
			return Dir{Path: filepath.Join(tmp, App), Source: FromFallback, Var: name}, nil
		}
		return Dir{}, errors.New("paths: neither XDG_RUNTIME_DIR nor TMPDIR is set")
	default:
		// logind's directory, when the variable was lost (su, some ssh
		// setups) but the directory is there and really the user's.
		run := filepath.Join("/run/user", strconv.Itoa(env.UID))
		if info, err := env.Stat(run); err == nil && info.IsDir() &&
			info.Mode().Perm() == 0o700 && env.OwnedByUser(info) {
			return Dir{Path: filepath.Join(run, App), Source: FromFallback, Var: name}, nil
		}
		return Dir{}, fmt.Errorf("paths: XDG_RUNTIME_DIR is unset and %s isn't a 0700 directory "+
			"owned by you; log in through a session that sets it, or export it", run)
	}
}

// ConfigFile is slop-bot-config.yaml.
func (p *Paths) ConfigFile() string { return filepath.Join(p.Config.Path, "slop-bot-config.yaml") }

// DefaultBriefing is where install writes the user's briefing stub.
func (p *Paths) DefaultBriefing() string { return filepath.Join(p.Config.Path, "briefing.md") }

// TicketsDB is local mode's ticket store: user data, so under XDG_DATA_HOME.
func (p *Paths) TicketsDB() string { return filepath.Join(p.Data.Path, "tickets.db") }

// StateDB is slop-bot's operational state.
func (p *Paths) StateDB() string { return filepath.Join(p.State.Path, "state.db") }

// AgentDir holds one agent's rendered briefing and other per-agent files.
func (p *Paths) AgentDir(id string) string { return filepath.Join(p.State.Path, "agents", id) }

// TranscriptsDir holds transcripts retained after their ticket ends.
func (p *Paths) TranscriptsDir() string { return filepath.Join(p.State.Path, "transcripts") }

// LogDir holds the daemon's logs.
func (p *Paths) LogDir() string { return filepath.Join(p.State.Path, "logs") }

// Socket is the control socket the CLI talks to the daemon over.
func (p *Paths) Socket() string { return filepath.Join(p.Runtime.Path, "slop-bot.sock") }

// LockFile stops a second daemon starting.
func (p *Paths) LockFile() string { return filepath.Join(p.Runtime.Path, "daemon.lock") }
