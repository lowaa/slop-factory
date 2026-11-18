package workspace_test

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/golden from the current rendering")

var goldenParams = map[string]workspace.Params{
	"plain": {AgentName: "builder-1", Server: "https://slop-haus.example"},
	// Everything the quoting has to survive, and a server with a path.
	"hostile-name": {AgentName: `it's "$(touch x)" \ $HOME`, Server: "http://localhost:8080/sub/"},
	// The manifest's values reach the script as quoted arguments.
	"with-repos": {AgentName: "builder-1", Server: "https://slop-haus.example", Repos: []workspace.Repo{
		{Path: "app", Remote: "github.com/acme/app"},
		{Path: `x'; touch y; '`, Remote: "-o $(touch z)"},
	}},
	// The root is slop-haus's: AGENTS.md and CLAUDE.md are written there.
	"nested-one-repo": {AgentName: "builder-1", Server: "https://slop-haus.example", Repos: []workspace.Repo{
		{Path: "app", Remote: "github.com/acme/app"},
	}},
	"nested-multi-repo": {AgentName: "builder-1", Server: "https://slop-haus.example", Repos: []workspace.Repo{
		{Path: "app", Remote: "github.com/acme/app"},
		{Path: "deploy", Remote: "github.com/acme/deploy"},
	}, Fleet: &workspace.FleetInstructions{Name: "house rules", Version: 1, Body: "Comment a plan first."}},
	// A person's session reads the same root, so its protocol is theirs, not the worker's.
	"nested-conductor": {AgentName: "rc-1", Server: "https://slop-haus.example", Role: workspace.RoleConductor, Repos: []workspace.Repo{
		{Path: "app", Remote: "github.com/acme/app"},
	}},
	"nested-console": {AgentName: "console-1", Server: "https://slop-haus.example", Role: workspace.RoleConsole, Repos: []workspace.Repo{
		{Path: "app", Remote: "github.com/acme/app"},
	}},
	// The fleet instructions land in three files in each script, quoted;
	// the text is hostile so the golden shows what happens to it.
	"with-fleet": {AgentName: "builder-1", Server: "https://slop-haus.example", Fleet: &workspace.FleetInstructions{
		Name: "house rules", Version: 2, Body: "Comment a plan first.\n\n## Working agreement\nSLOPHAUS_EOF\ntouch pwned",
	}},
}

// fullGolden is the one variant kept byte for byte, as an end-to-end check
// that the skeleton transform hides nothing but embedded asset bodies.
const fullGolden = "setup-plain"

var embeddedPlaceholder = regexp.MustCompile(`^<<embedded (\S+) sha256=([0-9a-f]{64})>>$`)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// embeddedAssets maps the sha256 of each embedded asset body to its path: a
// body (hooks, briefings, token.sh) that more than one golden variant writes
// to the same path. A body seen under one variant only -- the env file, the
// manifest, fleet instructions, a role's briefing -- stays in the skeleton,
// where its quoting is judged.
func embeddedAssets(t *testing.T) map[string]string {
	t.Helper()
	type key struct{ path, sha string }
	variants := map[key]int{}
	for name, p := range goldenParams {
		files, err := workspace.ScriptFiles(p)
		require.NoError(t, err, name)
		for _, f := range files {
			variants[key{f.Path, sha256Hex(f.Body)}]++
		}
	}
	assets := map[string]string{}
	for k, n := range variants {
		if n > 1 {
			assets[k.sha] = k.path
		}
	}
	return assets
}

// skeleton replaces each heredoc body that is an embedded asset with a
// placeholder carrying the body's hash, so an asset edit moves one line per
// script instead of rewriting it. A heredoc here is the form the scripts
// write files in: `cat > "$tmp" <<'TAG'`, the body, then TAG alone on a line.
func skeleton(t *testing.T, script string, assets map[string]string) string {
	t.Helper()
	lines := strings.SplitAfter(script, "\n")
	var out strings.Builder
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		out.WriteString(line)
		const opener = `cat > "$tmp" <<'`
		if !strings.HasPrefix(line, opener) {
			continue
		}
		tag := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(line, "\n"), opener), "'")
		end := -1
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSuffix(lines[j], "\n") == tag {
				end = j
				break
			}
		}
		require.NotEqual(t, -1, end, "unterminated heredoc %s", tag)
		body := strings.Join(lines[i+1:end], "")
		sha := sha256Hex([]byte(body))
		if path, ok := assets[sha]; ok {
			fmt.Fprintf(&out, "<<embedded %s sha256=%s>>\n", path, sha)
		} else {
			out.WriteString(body)
		}
		i = end - 1
	}
	return out.String()
}

// The rendered scripts are what reaches an agent's machine, so any byte of
// difference is a change in behaviour, however it came about -- a moved
// embed, a reordered file list, a touched comment. The other tests here
// judge the scripts by running them, which tolerates changes that don't
// alter the outcome; this one doesn't. A deliberate change regenerates the
// files with `go test ./internal/workspace -run TestGolden -update-golden`
// and the diff of testdata/golden shows a reviewer exactly what agents get.
//
// Each variant's golden is its skeleton, because every script repeats every
// embedded asset and one asset edit would otherwise rewrite them all.
// TestGoldenEmbeddedHashes ties the placeholders back to the assets, and
// the full golden covers the transform itself.
//
// The harness config is not an input here: run_remote_control.sh fetches it at run time,
// so no setup or update script varies with it.
func TestGoldenScripts(t *testing.T) {
	renderers := map[string]func(workspace.Params) (string, error){
		"setup":  workspace.Script,
		"update": workspace.UpdateScript,
	}
	assets := embeddedAssets(t)

	check := func(t *testing.T, golden, got string) {
		if *updateGolden {
			require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o755))
			require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
		}
		want, err := os.ReadFile(golden)
		require.NoError(t, err, "no golden file; generate it with -update-golden")
		// Equal on strings prints a diff, which says where the bytes moved.
		assert.Equal(t, string(want), got, "golden %s differs; if the change is intended, regenerate with: go test ./internal/workspace -run TestGolden -update-golden", golden)
	}

	for pname, p := range goldenParams {
		for rname, render := range renderers {
			name := rname + "-" + pname
			t.Run(name, func(t *testing.T) {
				got, err := render(p)
				require.NoError(t, err)
				check(t, filepath.Join("testdata", "golden", name+".sh"), skeleton(t, got, assets))
				if name == fullGolden {
					check(t, filepath.Join("testdata", "golden", "full", name+".sh"), got)
				}
			})
		}
	}
}

// A placeholder's hash must be that of the asset the binary embeds, so a
// golden left stale by an asset edit fails here by name, not just as a
// skeleton diff. Reading the goldens from disk is the point: under
// -update-golden they were just rewritten and are consistent.
func TestGoldenEmbeddedHashes(t *testing.T) {
	assets := embeddedAssets(t)
	require.NotEmpty(t, assets)
	paths, err := filepath.Glob(filepath.Join("testdata", "golden", "*-*.sh"))
	require.NoError(t, err)
	seen := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(line, "<<embedded ") {
				continue
			}
			m := embeddedPlaceholder.FindStringSubmatch(line)
			require.NotNil(t, m, "%s: malformed placeholder %q", path, line)
			assert.Equal(t, m[1], assets[m[2]], "%s: no embedded asset %s with hash %s (stale after an asset edit?)", path, m[1], m[2])
			seen++
		}
	}
	assert.NotZero(t, seen, "no placeholders found in the goldens")
}
