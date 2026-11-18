package claudecli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/lowaa/slop-factory/slop-review/internal/secrets"
)

// homeCredentials are where credentials usually live under a home
// directory. The review runs as the person or slot that started it, so its
// shell could otherwise read any of them.
var homeCredentials = []string{
	".ssh", ".aws", ".gnupg", ".kube", ".docker/config.json", ".netrc", ".git-credentials",
	".npmrc", ".pypirc", ".terraform.d", ".azure",
	".config/gh", ".config/glab", ".config/gcloud", ".pi", ".codex", ".claude/.credentials.json",
}

// loginDirEnv are the variables naming another tool's login directory. A
// supervisor points them at directories under its own root, outside $HOME,
// so homeCredentials alone would leave its Codex and pi auth.json readable.
// CLAUDE_CONFIG_DIR is not here: claude must read its own config dir
// (settings, session state), so only its .credentials.json is denied.
var loginDirEnv = []string{"CODEX_HOME", "PI_CODING_AGENT_DIR"}

// loginDirs resolves the login directories named by loginDirEnv. Unset or
// empty variables are skipped. A relative value is resolved against the
// slop-review's own working directory, and links
// are followed so the sandbox binds the real directory. A directory that
// is, or contains, the checkout is refused: denying it would hide the
// review's own repository.
func loginDirs(repoRoot string, getenv func(string) string) ([]string, error) {
	protected := []string{resolve(repoRoot), resolve(checkoutTop(repoRoot))}
	var dirs []string
	for _, name := range loginDirEnv {
		v := getenv(name)
		if v == "" {
			continue
		}
		dir, err := filepath.Abs(v)
		if err != nil {
			return nil, fmt.Errorf("claudecli: %s=%q: %w", name, v, err)
		}
		dir = resolve(dir)
		for _, p := range protected {
			if within(dir, p) {
				return nil, fmt.Errorf("claudecli: %s=%q contains the checkout %q; point it at a directory of its own", name, v, p)
			}
		}
		dirs = append(dirs, dir)
	}
	return dirs, nil
}

// walkSkipped are directories not searched for secret-shaped files: git's
// own, and dependency trees too large to walk for every session.
var walkSkipped = []string{".git", "node_modules"}

// readDenied lists what a review session may not read, by its tools or its
// shell: the secret-shaped files in the checkout holding repoRoot (the
// whole checkout, since a review scoped to a subdirectory can still read
// ../../.env) and directly in each directory above it (a slop-haus tenant
// keeps its secrets.env a few levels above its slots' checkouts), and the
// credential stores under home, Claude Code's own login among them. Only
// paths that exist: the sandbox binds over each one.
func readDenied(repoRoot, home, claudeConfigDir string, loginDirs []string) []string {
	top := checkoutTop(repoRoot)
	var denied []string
	_ = filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil
		case d.IsDir() && slices.Contains(walkSkipped, d.Name()):
			return filepath.SkipDir
		case secretShapedFile(path):
			denied = appendSecret(denied, path, d.Type())
		}
		return nil
	})
	for dir := filepath.Dir(top); ; dir = filepath.Dir(dir) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if path := filepath.Join(dir, e.Name()); secretShapedFile(path) {
				denied = appendSecret(denied, path, e.Type())
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	var stores []string
	if home != "" {
		for _, rel := range homeCredentials {
			stores = append(stores, filepath.Join(home, rel))
		}
	}
	stores = append(stores, loginDirs...)
	if claudeConfigDir != "" {
		stores = append(stores, filepath.Join(claudeConfigDir, ".credentials.json"))
	}
	for _, p := range stores {
		if _, err := os.Lstat(p); err == nil && !slices.Contains(denied, p) {
			denied = append(denied, p)
		}
	}
	return denied
}

// appendSecret adds a secret-shaped file to denied: a regular file as it
// is, and a link both as itself and as what it leads to, which may lie
// outside everything walked (a checkout's .env linked to a vault).
func appendSecret(denied []string, path string, mode fs.FileMode) []string {
	switch {
	case mode.IsRegular():
		return append(denied, path)
	case mode&fs.ModeSymlink != 0:
		denied = append(denied, path)
		if target, err := filepath.EvalSymlinks(path); err == nil && !slices.Contains(denied, target) {
			denied = append(denied, target)
		}
	}
	return denied
}

// secretShapedFile is the toolset's secret-shaped name, plus secrets.env,
// which is where slop-haus keeps a tenant's credentials.
func secretShapedFile(path string) bool {
	return secrets.LooksLikeSecret(path) || filepath.Base(path) == "secrets.env"
}

// checkoutTop is the nearest directory at or above dir holding .git, or dir
// itself outside a checkout.
func checkoutTop(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if d == filepath.Dir(d) {
			return dir
		}
	}
}

// readDenyRules are the permission rules that keep Claude Code's Read,
// Grep and Glob tools off denied; the sandbox's denyRead only covers the
// shell. "//" starts an absolute path in a rule.
func readDenyRules(denied []string) []string {
	rules := make([]string, 0, len(denied))
	for _, p := range denied {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			rules = append(rules, "Read(/"+p+"/**)")
			continue
		}
		rules = append(rules, "Read(/"+p+")")
	}
	return rules
}

// minRedactedLen keeps a short value from being "redacted" out of ordinary
// words.
const minRedactedLen = 16

// redactLogin removes claude's own login token from out. Unlike every other
// credential it can't be kept from the shell's environment, since claude
// signs in with it, so a review talked into quoting `env` would otherwise
// carry it into the report.
func redactLogin(out []byte) []byte {
	for _, name := range loginEnv {
		if v := os.Getenv(name); len(v) >= minRedactedLen {
			out = bytes.ReplaceAll(out, []byte(v), []byte("[redacted]"))
		}
	}
	return out
}
