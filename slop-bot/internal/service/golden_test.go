package service_test

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden from the current rendering")

// awkwardHome has the characters the unit and plist must escape in a path:
// a space (word splitting), % (systemd specifiers), $ (left literal in the
// executable path) and & < > (XML). systemd refuses quotes and backslashes
// in an executable path outright, so those go in awkwardPATH, through the
// quoted Environment= values.
const awkwardHome = `a b%h$x&<>`

const awkwardPATH = `:/opt/q"x\y/bin:relative:/usr/bin`

var goldenCases = []struct {
	name, goos, homeName, extraPATH string
}{
	{"systemd", "linux", "", ""},
	{"systemd_awkward", "linux", awkwardHome, awkwardPATH},
	{"launchd", "darwin", "", ""},
	{"launchd_awkward", "darwin", awkwardHome, awkwardPATH},
}

func TestRenderedFilesMatchGolden(t *testing.T) {
	t.Parallel()
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, &fixtureOpts{goos: tc.goos, homeName: tc.homeName, extraPATH: tc.extraPATH})
			_, err := f.mgr.Install(context.Background())
			require.NoError(t, err)
			got := f.stable(readFile(t, f.mgr.UnitPath()))
			golden := filepath.Join("testdata", tc.name+".golden")
			if *updateGolden {
				require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
			}
			assert.Equal(t, readFile(t, golden), got, "re-run with -update if the change is intended")
		})
	}
}

// systemd-analyze checks the unit really parses, that ExecStart names an
// executable (so the escaping round-trips to the real path), and that
// every setting is known. It reads unit directories, so the test points
// every XDG variable it reads at temp dirs.
func TestSystemdAnalyzeVerifiesTheUnit(t *testing.T) {
	t.Parallel()
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze not installed")
	}
	for _, homeName := range []string{"", awkwardHome} {
		t.Run(homeName, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, &fixtureOpts{goos: "linux", homeName: homeName, extraPATH: awkwardPATH})
			_, err := f.mgr.Install(context.Background())
			require.NoError(t, err)
			// Short, so the sockets of the stock user units the default
			// target pulls in fit sun_path, which keeps the output quiet.
			rt, err := os.MkdirTemp("", "sbsvc")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(rt) })
			cmd := exec.Command(analyze, "--user", "verify", f.mgr.UnitPath()) // #nosec G204 -- the test's own unit file
			cmd.Env = []string{
				"PATH=/usr/bin:/bin",
				"HOME=" + f.home,
				"XDG_CONFIG_HOME=" + filepath.Join(f.root, "cfg"),
				"XDG_DATA_HOME=" + filepath.Join(f.root, "data"),
				"XDG_RUNTIME_DIR=" + rt,
			}
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
			// verify exits 0 on settings it ignores (an unknown key, a bad
			// KillMode), so any line about our unit is a failure; lines
			// about the stock units the default target pulls in are not.
			for _, line := range strings.Split(string(out), "\n") {
				assert.NotContains(t, line, "slop-bot.service")
			}
		})
	}
}

func TestPlutilLintsThePlist(t *testing.T) {
	t.Parallel()
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil not installed (macOS only)")
	}
	for _, homeName := range []string{"", awkwardHome} {
		t.Run(homeName, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, &fixtureOpts{goos: "darwin", homeName: homeName, extraPATH: awkwardPATH})
			_, err := f.mgr.Install(context.Background())
			require.NoError(t, err)
			out, err := exec.Command(plutil, "-lint", f.mgr.UnitPath()).CombinedOutput() // #nosec G204 -- the test's own plist
			require.NoError(t, err, string(out))
		})
	}
}
