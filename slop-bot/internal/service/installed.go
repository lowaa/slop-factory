package service

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
)

// ErrNoServiceManager means the per-user service manager didn't answer: no
// systemd user session, or no launchd GUI domain (a login over ssh only).
var ErrNoServiceManager = errors.New("service: the per-user service manager isn't reachable")

// Mismatch is a location the CLI and the installed service resolve
// differently: the CLI then talks to a socket no daemon listens on, or
// reads files the daemon doesn't write.
type Mismatch struct {
	What    string
	CLI     string
	Service string
}

// Mismatches compares what this shell resolves with what the installed
// service does. The socket comes first: it is the one that breaks every
// client command.
func Mismatches(cli, svc *paths.Paths) []Mismatch {
	pairs := []Mismatch{
		{"control socket", cli.Socket(), svc.Socket()},
		{"config dir", cli.Config.Path, svc.Config.Path},
		{"data dir", cli.Data.Path, svc.Data.Path},
		{"state dir", cli.State.Path, svc.State.Path},
		{"cache dir", cli.Cache.Path, svc.Cache.Path},
	}
	var out []Mismatch
	for _, m := range pairs {
		if m.CLI != m.Service {
			out = append(out, m)
		}
	}
	return out
}

// ServicePaths is where the installed service's daemon keeps its files: the
// CLI compares it with its own to spot a shell whose XDG_* variables differ
// from the service's. A function rather than a Manager method: a method
// returning *paths.Paths would make deadcode count every Paths method as
// reachable through reflection.
func ServicePaths(m Manager) (*paths.Paths, error) {
	env, err := m.ServiceEnv()
	if err != nil {
		return nil, err
	}
	return paths.Resolve(env)
}

// installedEnv is the environment the daemon under the service resolves
// its paths in: the variables its unit or plist sets.
func installedEnv(set *settings, goos string, env map[string]string) *paths.Env {
	return &paths.Env{
		Lookup: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		Home:   set.Home, UID: set.UID, GOOS: goos,
		// Only reached when the file carries no runtime dir: a unit
		// written by hand. Never "owned", so it reports rather than
		// guesses.
		Stat:        os.Stat,
		OwnedByUser: func(os.FileInfo) bool { return false },
	}
}

func (s *systemd) ServiceEnv() (*paths.Env, error) {
	if err := s.requireInstalled(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.UnitPath())
	if err != nil {
		return nil, fmt.Errorf("service: reading %s: %w", s.UnitPath(), err)
	}
	env, err := parseSystemdEnv(raw)
	if err != nil {
		return nil, fmt.Errorf("service: %s: %w", s.UnitPath(), err)
	}
	// The unit leaves XDG_RUNTIME_DIR to systemd, which gives the service
	// logind's directory whatever this shell says.
	env["XDG_RUNTIME_DIR"] = filepath.Join("/run/user", strconv.Itoa(s.set.UID))
	return installedEnv(s.set, "linux", env), nil
}

// parseSystemdEnv reads the Environment= lines back, undoing systemdEnv's
// quoting: it reads only what this package writes, not every form systemd
// accepts.
func parseSystemdEnv(raw []byte) (map[string]string, error) {
	env := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "Environment=")
		if !ok {
			continue
		}
		assign, err := systemdUnquote(v)
		if err != nil {
			return nil, err
		}
		name, value, ok := strings.Cut(strings.ReplaceAll(assign, "%%", "%"), "=")
		if !ok {
			return nil, fmt.Errorf("environment line %q has no =", line)
		}
		env[name] = value
	}
	return env, nil
}

func systemdUnquote(v string) (string, error) {
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return "", fmt.Errorf("environment value %s isn't double-quoted", v)
	}
	var b strings.Builder
	in := v[1 : len(v)-1]
	for i := 0; i < len(in); i++ {
		if in[i] == '\\' && i+1 < len(in) {
			i++
		}
		b.WriteByte(in[i])
	}
	return b.String(), nil
}

func (l *launchd) ServiceEnv() (*paths.Env, error) {
	if err := requireFile(l.UnitPath()); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(l.UnitPath())
	if err != nil {
		return nil, fmt.Errorf("service: reading %s: %w", l.UnitPath(), err)
	}
	env, err := parsePlistEnv(raw)
	if err != nil {
		return nil, fmt.Errorf("service: %s: %w", l.UnitPath(), err)
	}
	return installedEnv(l.set, "darwin", env), nil
}

// plistNode is any plist element, for walking the document generically.
type plistNode struct {
	XMLName xml.Name
	Text    string      `xml:",chardata"`
	Nodes   []plistNode `xml:",any"`
}

// parsePlistEnv reads the EnvironmentVariables dict: the <key>/<string>
// pairs of the dict that follows that key in the top-level dict.
func parsePlistEnv(raw []byte) (map[string]string, error) {
	var doc struct {
		Dict plistNode `xml:"dict"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	top := doc.Dict.Nodes
	for i := 0; i+1 < len(top); i++ {
		if top[i].XMLName.Local != "key" || top[i].Text != "EnvironmentVariables" {
			continue
		}
		vars := top[i+1].Nodes
		env := map[string]string{}
		for j := 0; j+1 < len(vars); j += 2 {
			if vars[j].XMLName.Local != "key" || vars[j+1].XMLName.Local != "string" {
				return nil, errors.New("EnvironmentVariables isn't key/string pairs")
			}
			env[vars[j].Text] = vars[j+1].Text
		}
		return env, nil
	}
	return map[string]string{}, nil
}
