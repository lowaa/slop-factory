package workspace_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// In name order, so the golden's listing of the entries is stable.
	for _, name := range slices.Sorted(maps.Keys(files)) {
		body := files[name]
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// A package small enough to read in a golden file, with the shape the server ships.
func testArchive(t *testing.T) []byte {
	return tarGz(t, map[string]string{"pyproject.toml": "[project]\n", "src/pkg/__init__.py": "V = 1\n"})
}

const (
	archiveOpener = "archive_b64=$(cat <<'SLOPHAUS_ARCHIVE'\n"
	archiveCloser = "SLOPHAUS_ARCHIVE\n"
)

// unpackedArchive replaces the script's base64 package with a listing of the
// entries it unpacks to: each one's name, type, mode and bytes. The compressed
// bytes are compress/flate's to choose and change between Go releases, so
// comparing them would fail a toolchain bump that changed nothing an agent
// gets. The base64 body must still be the package wrapped as the script wraps
// it, so the encoding is judged too.
func unpackedArchive(t *testing.T, script string) string {
	t.Helper()
	start := strings.Index(script, archiveOpener)
	require.NotEqual(t, -1, start, "no archive heredoc")
	start += len(archiveOpener)
	length := strings.Index(script[start:], "\n"+archiveCloser)
	require.NotEqual(t, -1, length, "unterminated archive heredoc")
	body := script[start : start+length+1]

	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(body, "\n", ""))
	require.NoError(t, err)
	assert.Equal(t, wrap76(base64.StdEncoding.EncodeToString(raw)), body, "the package is base64 in 76-column lines")
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	require.NoError(t, err)
	defer func() { _ = gz.Close() }()
	var listing strings.Builder
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		content, err := io.ReadAll(tr)
		require.NoError(t, err)
		fmt.Fprintf(&listing, "<<archive entry %q type=%q mode=%04o bytes=%q>>\n", h.Name, h.Typeflag, h.Mode, content)
	}
	return script[:start] + listing.String() + script[start+len(body):]
}

func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76] + "\n")
		s = s[76:]
	}
	return b.String() + s + "\n"
}

func TestGoldenSupervisorScript(t *testing.T) {
	cases := map[string]workspace.SupervisorParams{
		"plain": {Server: "https://slop-haus.example/", TenantID: "11111111-2222-3333-4444-555555555555", TenantName: "Acme Corp"},
		// Both values are data in the script, however hostile.
		"hostile": {Server: `http://x/'; touch pwned; '`, TenantID: `$(touch pwned)`, TenantName: "$(touch pwned)'"},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			p.Archive = testArchive(t)
			got, err := workspace.SupervisorScript(p)
			require.NoError(t, err)

			got = unpackedArchive(t, got)
			golden := filepath.Join("testdata", "golden", "supervisor-setup-"+name+".sh")
			if *updateGolden {
				require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err, "no golden file; generate it with -update-golden")
			assert.Equal(t, string(want), got)
		})
	}
}

func TestSupervisorScriptRefusesWhatItCannotCarry(t *testing.T) {
	ok := workspace.SupervisorParams{Server: "https://s", TenantID: "t", Archive: []byte("x")}
	for name, mutate := range map[string]func(*workspace.SupervisorParams){
		"no server":       func(p *workspace.SupervisorParams) { p.Server = "" },
		"line break":      func(p *workspace.SupervisorParams) { p.TenantID = "a\nb" },
		"no tenant":       func(p *workspace.SupervisorParams) { p.TenantID = "" },
		"no archive":      func(p *workspace.SupervisorParams) { p.Archive = nil },
		"carriage return": func(p *workspace.SupervisorParams) { p.Server = "https://s\r" },
	} {
		t.Run(name, func(t *testing.T) {
			p := ok
			mutate(&p)
			_, err := workspace.SupervisorScript(p)
			assert.Error(t, err)
		})
	}
}

// The values and the package that go in come out the other side intact: the
// script is run with a uv that unpacks nothing itself but records what init
// was told and lists the package it was handed.
func TestSupervisorScriptDeliversItsInputs(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	// uv: skip "run --no-project --python SPEC python", then run python3 -S (so a
	// supervisor installed in the ambient python cannot shadow the stand-in) -- the
	// stand-in module lists the package it was given and echoes the rest.
	require.NoError(t, os.WriteFile(filepath.Join(bin, "uv"), []byte("#!/usr/bin/env bash\nshift 5\nexec python3 -S \"$@\"\n"), 0o755))

	module := "import sys, tarfile\nprint('ARGS', *sys.argv[1:])\nprint('MEMBERS', sorted(tarfile.open(sys.argv[sys.argv.index('--package') + 1]).getnames()))\n"
	archive := tarGz(t, map[string]string{"pyproject.toml": "[project]\n", "src/slop_haus_supervisor/__main__.py": module})

	script, err := workspace.SupervisorScript(workspace.SupervisorParams{
		Server: "https://slop-haus.example/", TenantID: `$(touch ` + filepath.Join(dir, "pwned") + `)`, Archive: archive,
	})
	require.NoError(t, err)

	cmd := exec.Command("bash", "-c", script, "setup", "--root", filepath.Join(dir, "root"), "--tenant", "acme")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	assert.Contains(t, string(out), "ARGS init --server https://slop-haus.example --tenant-id $(touch "+filepath.Join(dir, "pwned")+") --tenant acme")
	assert.Contains(t, string(out), "MEMBERS ['pyproject.toml', 'src/slop_haus_supervisor/__main__.py']")
	assert.NoFileExists(t, filepath.Join(dir, "pwned"))
}

func TestSlugTenantName(t *testing.T) {
	for in, want := range map[string]string{
		"Acme Corp":        "acme-corp",
		"  --Acme__Corp!!": "acme-corp",
		"Ünïcode co":       "n-code-co",
		"日本語":              "",
		"":                 "",
		"a'; touch x; '":   "a-touch-x",
		"Team 42":          "team-42",
	} {
		assert.Equal(t, want, workspace.SlugTenantName(in), in)
	}
}

// With no --tenant the name the server suggested goes to init as a suggestion,
// so init can keep it from landing on another tenant's folder; an empty one
// falls back to the host's first label.
func TestSupervisorScriptSuggestsTheServersTenantName(t *testing.T) {
	for name, tc := range map[string]struct{ tenantName, want string }{
		"from the server":              {"Acme Corp", "--default-tenant acme-corp"},
		"empty falls back to the host": {"日本語", "--default-tenant slop-haus"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			require.NoError(t, os.Mkdir(bin, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "uv"), []byte("#!/usr/bin/env bash\nshift 5\nexec python3 -S \"$@\"\n"), 0o755))
			module := "import sys\nprint('ARGS', *sys.argv[1:])\n"
			archive := tarGz(t, map[string]string{"pyproject.toml": "[project]\n", "src/slop_haus_supervisor/__main__.py": module})
			script, err := workspace.SupervisorScript(workspace.SupervisorParams{
				Server: "https://slop-haus.example/", TenantID: "t", TenantName: tc.tenantName, Archive: archive,
			})
			require.NoError(t, err)

			cmd := exec.Command("bash", "-c", script, "setup", "--root", filepath.Join(dir, "root"))
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))

			assert.Contains(t, string(out), tc.want)
			assert.NotContains(t, string(out), " --tenant ")
		})
	}
}
