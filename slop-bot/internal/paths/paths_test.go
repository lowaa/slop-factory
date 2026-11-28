package paths

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeInfo struct {
	dir  bool
	mode fs.FileMode
}

func (f fakeInfo) Name() string       { return "x" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

func env(vars map[string]string, goos string) *Env {
	return &Env{
		Lookup:      func(k string) (string, bool) { v, ok := vars[k]; return v, ok },
		Home:        "/home/u",
		UID:         1000,
		GOOS:        goos,
		Stat:        func(string) (os.FileInfo, error) { return nil, errors.New("absent") },
		OwnedByUser: func(os.FileInfo) bool { return true },
	}
}

func TestDefaultsWhenNothingIsSet(t *testing.T) {
	t.Parallel()
	p, err := Resolve(env(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, "linux"))
	require.NoError(t, err)
	assert.Equal(t, Dir{Path: "/home/u/.config/slop-bot", Source: FromDefault, Var: "XDG_CONFIG_HOME"}, p.Config)
	assert.Equal(t, "/home/u/.local/share/slop-bot/tickets.db", p.TicketsDB())
	assert.Equal(t, "/home/u/.local/state/slop-bot/state.db", p.StateDB())
	assert.Equal(t, "/home/u/.cache/slop-bot", p.Cache.Path)
	assert.Equal(t, "/run/user/1000/slop-bot/slop-bot.sock", p.Socket())
	assert.Equal(t, FromEnv, p.Runtime.Source)
}

func TestXDGVariablesWin(t *testing.T) {
	t.Parallel()
	p, err := Resolve(env(map[string]string{
		"XDG_CONFIG_HOME": "/c", "XDG_DATA_HOME": "/d", "XDG_STATE_HOME": "/s",
		"XDG_CACHE_HOME": "/k", "XDG_RUNTIME_DIR": "/r",
	}, "linux"))
	require.NoError(t, err)
	assert.Equal(t, "/c/slop-bot/slop-bot-config.yaml", p.ConfigFile())
	assert.Equal(t, "/d/slop-bot", p.Data.Path)
	assert.Equal(t, "/s/slop-bot/agents/a1", p.AgentDir("a1"))
	assert.Equal(t, "/k/slop-bot", p.Cache.Path)
	assert.Equal(t, "/r/slop-bot/daemon.lock", p.LockFile())
	assert.Equal(t, FromEnv, p.Data.Source)
}

func TestRelativeXDGValuesAreIgnored(t *testing.T) {
	t.Parallel()
	p, err := Resolve(env(map[string]string{"XDG_CONFIG_HOME": "rel", "XDG_RUNTIME_DIR": "/r"}, "linux"))
	require.NoError(t, err)
	assert.Equal(t, FromDefault, p.Config.Source)
}

func TestLinuxFallsBackToRunUser(t *testing.T) {
	t.Parallel()
	e := env(map[string]string{}, "linux")
	e.Stat = func(name string) (os.FileInfo, error) {
		if name == "/run/user/1000" {
			return fakeInfo{dir: true, mode: fs.ModeDir | 0o700}, nil
		}
		return nil, errors.New("absent")
	}
	p, err := Resolve(e)
	require.NoError(t, err)
	assert.Equal(t, Dir{Path: "/run/user/1000/slop-bot", Source: FromFallback, Var: "XDG_RUNTIME_DIR"}, p.Runtime)
}

func TestLinuxFallbackRefusesAnUnsafeDirectory(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		mode  fs.FileMode
		owned bool
	}{
		"world readable": {fs.ModeDir | 0o755, true},
		"someone else's": {fs.ModeDir | 0o700, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := env(map[string]string{}, "linux")
			e.Stat = func(string) (os.FileInfo, error) { return fakeInfo{dir: true, mode: tc.mode}, nil }
			e.OwnedByUser = func(os.FileInfo) bool { return tc.owned }
			_, err := Resolve(e)
			assert.ErrorContains(t, err, "XDG_RUNTIME_DIR is unset")
		})
	}
}

func TestMacOSFallsBackToTMPDIR(t *testing.T) {
	t.Parallel()
	p, err := Resolve(env(map[string]string{"TMPDIR": "/var/folders/xy/T/"}, "darwin"))
	require.NoError(t, err)
	assert.Equal(t, "/var/folders/xy/T/slop-bot/slop-bot.sock", p.Socket())
	assert.Equal(t, FromFallback, p.Runtime.Source)
}

func TestSocketPathOverTheLimitIsRefused(t *testing.T) {
	t.Parallel()
	long := "/" + strings.Repeat("x", 90)
	_, err := Resolve(env(map[string]string{"XDG_RUNTIME_DIR": long}, "darwin"))
	assert.ErrorContains(t, err, "over the 103-byte limit")
}

func TestNoHomeIsAnError(t *testing.T) {
	t.Parallel()
	e := env(nil, "linux")
	e.Home = ""
	_, err := Resolve(e)
	assert.Error(t, err)
}

func TestFromOSResolves(t *testing.T) {
	t.Parallel()
	e, err := FromOS()
	require.NoError(t, err)
	assert.NotEmpty(t, e.Home)
}
