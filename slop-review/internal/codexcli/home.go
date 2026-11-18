package codexcli

import (
	"fmt"
	"os"
	"path/filepath"
)

// PrepareIsolatedHome builds a CODEX_HOME at dir holding the user's login
// and nothing else of theirs, and returns its path.
//
// --ignore-user-config only skips config.toml. The rest of CODEX_HOME still
// reaches the model -- a global AGENTS.md, skills, memories -- and any of
// it would sit alongside the persona as instructions slop-review never
// chose.
//
// dir must be empty or not yet exist: anything already there would reach
// the model just the same.
//
// The login is symlinked, so a re-login is picked up without rebuilding
// anything, and slop-review never copies a credential.
func PrepareIsolatedHome(dir string) (string, error) {
	source := os.Getenv("CODEX_HOME")
	if source == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("codexcli: resolve home directory: %w", err)
		}
		source = filepath.Join(home, ".codex")
	}
	// Absolute, because the auth.json symlink stores it verbatim and Codex
	// would resolve a relative one against the isolated home instead.
	source, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("codexcli: resolve CODEX_HOME %s: %w", source, err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("codexcli: resolve %s: %w", dir, err)
	}
	// Compared by where each path really lands: were they the same
	// directory, linkIfPresent would delete the real auth.json to put a
	// symlink to itself in its place.
	if canonical(abs) == canonical(source) {
		return "", fmt.Errorf("codexcli: the isolated home would be the real one (%s)", source)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("codexcli: create isolated home: %w", err)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", fmt.Errorf("codexcli: read isolated home: %w", err)
	}
	if len(entries) > 0 {
		return "", fmt.Errorf("codexcli: the isolated home %s must be empty, or Codex would read what is already there", abs)
	}
	if err := linkIfPresent(filepath.Join(source, "auth.json"), filepath.Join(abs, "auth.json")); err != nil {
		return "", fmt.Errorf("codexcli: link auth.json: %w", err)
	}
	return abs, nil
}

// canonical resolves path to an absolute, symlink-free form, as far as it
// exists; a path that doesn't exist yet can't alias anything.
func canonical(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// linkIfPresent points dst at src. A missing src is not an error: a login
// by environment variable has no auth.json.
func linkIfPresent(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.Symlink(src, dst)
}
