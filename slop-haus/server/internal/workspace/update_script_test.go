package workspace_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// Like the setup script, the update script is only ever judged by running
// it -- here, over a workspace the setup script built, which is the only
// kind it is ever pointed at.

var updateParams = workspace.Params{
	AgentName: "builder-1",
	Server:    "https://slop-haus.example",
}

func runUpdate(t *testing.T, dir string, p workspace.Params) (string, error) {
	t.Helper()
	script, err := workspace.UpdateScript(p)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "update.sh")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))

	cmd := exec.Command("bash", path)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustUpdate(t *testing.T, dir string, p workspace.Params) {
	t.Helper()
	out, err := runUpdate(t, dir, p)
	require.NoError(t, err, "update failed:\n%s", out)
}

// setUp builds a workspace with the setup script, as a human would.
func setUp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runScript(t, dir, updateParams)
	require.NoError(t, os.Remove(filepath.Join(dir, "setup.sh")))
	return dir
}

// snapshot reads every file under dir, keyed by relative path, with its mode.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = info.Mode().Perm().String() + "\n" + string(body)
		return nil
	}))
	return files
}

// The update carries no secret of its own -- there is nothing left in
// EnvFile worth protecting, so the old "the update must never write the
// env file" rule is gone along with the token it existed to protect. See
// TestUpdateScript_DropsAnOlderWorkspacesDeadTokenLine.
func TestUpdateScript_CarriesNoSecret(t *testing.T) {
	script, err := workspace.UpdateScript(updateParams)
	require.NoError(t, err)

	assert.NotContains(t, script, "SLOP_HAUS_TOKEN=",
		"the dead token line must never be reintroduced")
}

// update.sh refuses anything whose second line is not this, so an
// error page served with a 200 is never run. The two sides are pinned
// together here: change one and this fails.
func TestUpdateScript_StartsWithTheMarkerTheUpdaterChecks(t *testing.T) {
	script, err := workspace.UpdateScript(updateParams)
	require.NoError(t, err)

	lines := strings.SplitN(script, "\n", 3)
	require.Len(t, lines, 3)
	assert.Equal(t, "#!/usr/bin/env bash", lines[0])
	assert.Equal(t, "# slop-haus workspace update", lines[1])

	updater := fileBody(t, workspace.Updater)
	assert.Contains(t, updater, `!= "`+lines[1]+`"`,
		"update.sh must check for exactly the marker the update script carries")
}

// The point of the whole feature: a change to what slop-haus manages
// reaches a workspace that already exists.
func TestUpdateScript_RewritesEveryManagedFile(t *testing.T) {
	dir := setUp(t)
	want := snapshot(t, dir)

	files, err := workspace.Files(updateParams)
	require.NoError(t, err)
	managed := 0
	for _, f := range files {
		if f.Ownership != workspace.Managed {
			continue
		}
		managed++
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		require.NoError(t, os.WriteFile(path, []byte("stale\n"), 0o600))
		// WriteFile keeps an existing file's mode, so without this the test
		// could not tell an update that forgot chmod from one that did not.
		require.NoError(t, os.Chmod(path, 0o600))
	}
	require.NotZero(t, managed)

	mustUpdate(t, dir, updateParams)

	assert.Equal(t, want, snapshot(t, dir),
		"every managed file -- contents and mode -- is back to what setup wrote")
}

// A workspace from before the updater existed is the
// case this is first needed for.
func TestUpdateScript_AddsWhatAnOlderWorkspaceLacks(t *testing.T) {
	dir := setUp(t)
	want := snapshot(t, dir)
	for _, rel := range []string{workspace.Updater, workspace.ServerURL} {
		require.NoError(t, os.Remove(filepath.Join(dir, filepath.FromSlash(rel))))
	}

	mustUpdate(t, dir, updateParams)

	assert.Equal(t, want, snapshot(t, dir))
}

// An older workspace's .slop-haus.env still has the dead SLOP_HAUS_TOKEN
// line from before agents were authorized through OAuth. There is nothing
// secret left in that file to protect by leaving it alone, and every
// reason to drop the line: EnvFile is rewritten wholesale like any other
// managed file, which is what migrates an older workspace forward.
func TestUpdateScript_DropsAnOlderWorkspacesDeadTokenLine(t *testing.T) {
	dir := setUp(t)
	env := filepath.Join(dir, workspace.EnvFile)
	require.NoError(t, os.WriteFile(env, []byte("# slop-haus agent token. Do not commit.\nSLOP_HAUS_TOKEN=the-old-one\n"), 0o600))

	mustUpdate(t, dir, updateParams)

	body := fileIn(t, dir, workspace.EnvFile)
	assert.NotContains(t, body, "SLOP_HAUS_TOKEN")
	assert.Contains(t, body, "SLOP_HAUS_URL=")
	info, err := os.Stat(env)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "no secret is left in it to protect")
}

// This workspace's OAuth credentials -- .slop-haus/credentials.json -- are
// the one thing an update must never touch. Files doesn't list them at
// all, which is what makes this true by construction; this is the test
// that would catch it if that ever changed.
func TestUpdateScript_LeavesTheOAuthCredentialsAlone(t *testing.T) {
	dir := setUp(t)
	creds := filepath.Join(dir, ".slop-haus", "credentials.json")
	require.NoError(t, os.WriteFile(creds, []byte(`{"access_token":"the-real-one"}`), 0o600))

	mustUpdate(t, dir, updateParams)

	assert.Equal(t, `{"access_token":"the-real-one"}`, fileIn(t, dir, ".slop-haus/credentials.json"))
	info, err := os.Stat(creds)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// Claude Code writes the human's "always allow" answers here. Rewriting it
// would take every one of them away without a word.
func TestUpdateScript_KeepsTheWorkspacesOwnLocalSettings(t *testing.T) {
	dir := setUp(t)
	own := `{"permissions":{"allow":["mcp__slop-haus__*","Bash(go test:*)"]}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(workspace.LocalSettings)), []byte(own), 0o644))

	mustUpdate(t, dir, updateParams)

	assert.Equal(t, own, fileIn(t, dir, workspace.LocalSettings))
}

// run.env names the workspace's own secrets for the agent; an update that
// reset it would silently strip them from the next session.
func TestUpdateScript_KeepsTheWorkspacesOwnLauncherEnv(t *testing.T) {
	dir := setUp(t)
	own := "< .env\nEXTRA=1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(workspace.LauncherEnv)), []byte(own), 0o600))

	mustUpdate(t, dir, updateParams)

	assert.Equal(t, own, fileIn(t, dir, workspace.LauncherEnv))
}

// updateWithSeededFileRemoved deletes seeded file rel from a fresh
// workspace, then runs an update with the given stub commands first on
// PATH. Each stub is a bash body; $REAL is the real command it stands in
// for, and $TARGET the seeded file's absolute path.
//
// A stub mktemp is how these tests reach the race deterministically: the
// update calls it after checking the file is missing and before putting
// the template in place, so whatever the stub does lands in exactly that
// window, every run -- no timing involved.
func updateWithSeededFileRemoved(t *testing.T, rel string, stubs map[string]string) (dir, out string, err error) {
	t.Helper()
	dir = setUp(t)
	target := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.Remove(target))

	bin := t.TempDir()
	for name, body := range stubs {
		real, lookErr := exec.LookPath(name)
		require.NoError(t, lookErr)
		script := fmt.Sprintf("#!/usr/bin/env bash\nREAL=%s\nTARGET=%s\n%s\n",
			workspace.ShellQuote(real), workspace.ShellQuote(target), body)
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755))
	}

	script, genErr := workspace.UpdateScript(updateParams)
	require.NoError(t, genErr)
	path := filepath.Join(t.TempDir(), "update.sh")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	cmd := exec.Command("bash", path)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	outBytes, err := cmd.CombinedOutput()
	return dir, string(outBytes), err
}

// plantDuringMktemp is a mktemp stub that runs plant (bash, with $TARGET
// set) right after creating the seeded file's temporary copy.
func plantDuringMktemp(rel, plant string) map[string]string {
	return map[string]string{"mktemp": fmt.Sprintf(`tmp=$("$REAL" "$@") || exit
case "$1" in
  %s.*) %s ;;
esac
printf '%%s\n' "$tmp"`, workspace.ShellQuote(rel), plant)}
}

func assertNoTemplateLeftBehind(t *testing.T, dir, rel string) {
	t.Helper()
	leftovers, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(rel)) + ".*")
	require.NoError(t, err)
	assert.Empty(t, leftovers, "the unused template must not be left behind")
}

var seededFiles = []string{workspace.LocalSettings, workspace.LauncherEnv}

// Re-creating a missing seeded file must never replace one that appears
// while the update runs -- the file is the workspace's once it exists.
func TestUpdateScript_ASeededFileCreatedMidUpdateIsKept(t *testing.T) {
	for _, rel := range seededFiles {
		t.Run(rel, func(t *testing.T) {
			dir, out, err := updateWithSeededFileRemoved(t, rel,
				plantDuringMktemp(rel, `printf 'written by someone else mid-update\n' > "$TARGET"`))
			require.NoError(t, err, "update failed:\n%s", out)

			assert.Equal(t, "written by someone else mid-update\n", fileIn(t, dir, rel))
			assertNoTemplateLeftBehind(t, dir, rel)
		})
	}
}

// ln treats a directory target as "link inside it". One appearing in the
// window must be left as it is, not gain a stray copy of the template.
func TestUpdateScript_ADirectoryAppearingMidUpdateGetsNothingLinkedInside(t *testing.T) {
	dir, out, err := updateWithSeededFileRemoved(t, workspace.LauncherEnv,
		plantDuringMktemp(workspace.LauncherEnv, `mkdir "$TARGET"`))
	require.NoError(t, err, "update failed:\n%s", out)

	target := filepath.Join(dir, filepath.FromSlash(workspace.LauncherEnv))
	info, err := os.Lstat(target)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "the directory that appeared is the workspace's; it stays")
	entries, err := os.ReadDir(target)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assertNoTemplateLeftBehind(t, dir, workspace.LauncherEnv)
}

// The same through a link planted after refuse_links ran: nothing may be
// left behind in the directory it leads to, outside the workspace.
func TestUpdateScript_ALinkToAnOutsideDirectoryAppearingMidUpdateGetsNothingLeftInIt(t *testing.T) {
	outside := t.TempDir()
	dir, out, err := updateWithSeededFileRemoved(t, workspace.LauncherEnv,
		plantDuringMktemp(workspace.LauncherEnv, "ln -s "+workspace.ShellQuote(outside)+` "$TARGET"`))
	require.NoError(t, err, "update failed:\n%s", out)

	link, err := os.Readlink(filepath.Join(dir, filepath.FromSlash(workspace.LauncherEnv)))
	require.NoError(t, err, "the link that appeared must still be there, not replaced")
	assert.Equal(t, outside, link)
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing may be written outside the workspace")
	assertNoTemplateLeftBehind(t, dir, workspace.LauncherEnv)
}

// ln failing for any other reason, with nothing at the path, is a real
// failure: the update must say so rather than finish without the file.
func TestUpdateScript_FailsWhenASeededFileCannotBeCreated(t *testing.T) {
	dir, out, err := updateWithSeededFileRemoved(t, workspace.LauncherEnv,
		map[string]string{"ln": `echo "ln: permission denied" >&2; exit 1`})
	require.Error(t, err)
	assert.Contains(t, out, "could not create")
	assert.NoFileExists(t, filepath.Join(dir, filepath.FromSlash(workspace.LauncherEnv)))
	assertNoTemplateLeftBehind(t, dir, workspace.LauncherEnv)
}

func TestUpdateScript_RestoresLocalSettingsThatWentMissing(t *testing.T) {
	dir := setUp(t)
	want := fileIn(t, dir, workspace.LocalSettings)
	require.NoError(t, os.Remove(filepath.Join(dir, filepath.FromSlash(workspace.LocalSettings))))

	mustUpdate(t, dir, updateParams)

	assert.Equal(t, want, fileIn(t, dir, workspace.LocalSettings))
}

// Without the token file this is not a workspace, and hooks written into it
// would fail open on every turn with no credential behind them.
func TestUpdateScript_RefusesADirectoryThatIsNotAWorkspace(t *testing.T) {
	dir := t.TempDir()

	out, err := runUpdate(t, dir, updateParams)

	require.Error(t, err)
	assert.Contains(t, out, "not a slop-haus workspace")
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "nothing is written into a directory that is not a workspace")
}

func TestUpdateScript_RunningItTwiceIsRunningItOnce(t *testing.T) {
	dir := setUp(t)
	mustUpdate(t, dir, updateParams)
	once := snapshot(t, dir)

	mustUpdate(t, dir, updateParams)

	assert.Equal(t, once, snapshot(t, dir))
}

// The name is the one thing in the script a caller chose. Same rule as
// setup: quoted everywhere, and a line break refused outright.
func TestUpdateScript_DoesNotLetAnAgentNameEscapeIntoTheShell(t *testing.T) {
	dir := setUp(t)
	marker := filepath.Join(t.TempDir(), "pwned")
	p := updateParams
	p.AgentName = `x'; touch ` + marker + `; echo '$(touch ` + marker + `)`

	mustUpdate(t, dir, p)

	assert.NoFileExists(t, marker)
}

func TestUpdateScript_RefusesANameWithALineBreak(t *testing.T) {
	p := updateParams
	p.AgentName = "builder\ntouch /tmp/x"

	_, err := workspace.UpdateScript(p)

	require.Error(t, err)
}

// A checkout can carry symlinks, and the updater runs unattended. Every
// component of every path it writes is checked -- a link at the leaf, or at
// a parent like .claude, would otherwise put the file wherever it points.
func TestUpdateScript_RefusesToWriteThroughASymlink(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, dir, outside string){
		"a linked file": func(t *testing.T, dir, outside string) {
			link := filepath.Join(dir, filepath.FromSlash(workspace.HeadlessBriefing))
			require.NoError(t, os.Remove(link))
			require.NoError(t, os.Symlink(filepath.Join(outside, "victim"), link))
		},
		"a linked parent directory": func(t *testing.T, dir, outside string) {
			require.NoError(t, os.RemoveAll(filepath.Join(dir, ".claude")))
			require.NoError(t, os.Symlink(outside, filepath.Join(dir, ".claude")))
		},
		"a linked token file": func(t *testing.T, dir, outside string) {
			env := filepath.Join(dir, workspace.EnvFile)
			require.NoError(t, os.Rename(env, filepath.Join(outside, "victim")))
			require.NoError(t, os.Symlink(filepath.Join(outside, "victim"), env))
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := setUp(t)
			outside := t.TempDir()
			plant(t, dir, outside)
			before := snapshot(t, outside)
			// Something for a write that got through to clobber.
			require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(workspace.ServerURL)), []byte("stale\n"), 0o644))

			out, err := runUpdate(t, dir, updateParams)

			require.Error(t, err)
			assert.Contains(t, out, "is a symlink")
			assert.Equal(t, before, snapshot(t, outside), "nothing was written outside the workspace")
			assert.Equal(t, "stale\n", fileIn(t, dir, workspace.ServerURL),
				"a refusal happens before any write, not partway through")
		})
	}
}

// Files are written beside their target and renamed into place. A
// predictable temporary name would be one more thing to plant a link at;
// mktemp's is not, and none may be left behind.
func TestUpdateScript_LeavesNoTemporaryFilesBehind(t *testing.T) {
	dir := setUp(t)
	before := snapshot(t, dir)

	mustUpdate(t, dir, updateParams)

	assert.Equal(t, before, snapshot(t, dir))
}

// CLAUDE.md is a managed file like any other: a link there is refused rather
// than written through.
func TestUpdateScript_RefusesAClaudeMdLink(t *testing.T) {
	dir := setUp(t)
	outside := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(outside, []byte("someone else's\n"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(dir, workspace.ClaudeMD)))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, workspace.ClaudeMD)))
	require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(workspace.ServerURL)), []byte("stale\n"), 0o644))
	want := snapshot(t, dir)

	out, err := runUpdate(t, dir, updateParams)

	require.Error(t, err)
	assert.Contains(t, out, "is a symlink")
	assert.Equal(t, "someone else's\n", readFile(t, outside))
	assert.Equal(t, want, snapshot(t, dir), "refused before anything in the workspace was written")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(body)
}

// Workspaces from before the launcher moved to the supervisor still have the
// stubs; an update deletes them, and leaves the workspace's own files be.
func TestUpdateScript_RemovesTheRetiredStubs(t *testing.T) {
	dir := setUp(t)
	for _, rel := range workspace.RetiredFiles {
		require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte("#!/bin/sh\n"), 0o755))
	}

	mustUpdate(t, dir, updateParams)

	for _, rel := range workspace.RetiredFiles {
		assert.NoFileExists(t, filepath.Join(dir, filepath.FromSlash(rel)))
	}
	assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(workspace.Updater)))
}

// The interactive pi worker's briefing files are retired by name, so an old
// workspace stops carrying text that tells a session to work tickets itself.
func TestRetiredFiles_IncludeTheOldBriefings(t *testing.T) {
	assert.Contains(t, workspace.RetiredFiles, ".slop-haus/briefing.md")
	assert.Contains(t, workspace.RetiredFiles, ".slop-haus/pi/briefing.md")
}

func TestUpdateScript_RetiredStubsAbsentIsFine(t *testing.T) {
	mustUpdate(t, setUp(t), updateParams)
}

// A link where a stub used to be is refused like any other write path, before
// anything is removed or rewritten.
func TestUpdateScript_RefusesALinkAtARetiredPath(t *testing.T) {
	dir := setUp(t)
	victim := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(victim, []byte("mine\n"), 0o644))
	require.NoError(t, os.Symlink(victim, filepath.Join(dir, filepath.FromSlash(workspace.RetiredFiles[0]))))

	out, err := runUpdate(t, dir, updateParams)

	require.Error(t, err)
	assert.Contains(t, out, "is a symlink")
	assert.FileExists(t, victim)
}
