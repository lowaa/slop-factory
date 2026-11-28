package codex

// Why agents share the user's CODEX_HOME, and what that costs.
//
// Codex's TUI writes `[projects."<cwd>"] trust_level = "trusted"` into
// $CODEX_HOME/config.toml the first time it runs in a folder, even with
// approvals bypassed (TUI spike), so each attach adds an entry per worktree.
// app-server writes none (protocol spike). A per-agent CODEX_HOME holding a
// copy of auth.json, as slop-haus's supervisor uses, would keep those out,
// but costs more than it saves here:
//
//   - The login. ChatGPT refresh tokens rotate, and presenting a spent one
//     revokes the whole token family. Codex serialises refreshes between its
//     processes that share a home (auth.json.lock); a copy in another home
//     is outside that lock. The first time an agent's Codex refreshed, the
//     user's own ~/.codex would hold a spent token, and the next refresh,
//     theirs or another agent's, would log every session out. The
//     supervisor avoids that with a login of its own, refreshed by the
//     supervisor alone: setup, which slop-bot's users are promised they
//     don't need.
//   - The configuration. The user's model, MCP servers, profiles, global
//     AGENTS.md and skills live in that home; a copy drifts from them.
//   - History. The user's own `codex resume` lists agents' threads, and an
//     attach needs no extra environment to find them.
//
// So agents run on the user's home, and Forget removes the trust entries an
// attach left for a worktree when slop-bot removes it.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// forgetAttempts bounds the retries when Codex rewrites config.toml while
// Forget edits it.
const forgetAttempts = 3

// TrustFile is the trust section of a Codex config.toml.
type TrustFile struct {
	// Path is the config.toml.
	Path string
	// beforeCheck runs just before the file is re-read to see whether it
	// changed: a test's stand-in for Codex writing it then.
	beforeCheck func()
}

// ConfigPath is the config.toml that Codex run with environ reads:
// $CODEX_HOME's, else ~/.codex's.
func ConfigPath(environ []string) (string, error) {
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "CODEX_HOME="); ok && v != "" {
			return filepath.Join(v, "config.toml"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// ForgetWorkspace removes the trust entries Codex's TUI wrote for ws's
// directories, for when slop-bot removes them. Only call it for
// directories slop-bot created: an entry the user made for their own
// checkout is theirs.
func (h *Harness) ForgetWorkspace(ws *harness.Workspace) error {
	base := os.Environ
	if h.Environ != nil {
		base = h.Environ
	}
	path, err := ConfigPath(base())
	if err != nil {
		return err
	}
	_, err = (&TrustFile{Path: path}).Forget(append([]string{ws.Dir}, ws.ExtraDirs...)...)
	return err
}

// Forget removes the `[projects."<dir>"]` tables of dirs and returns the
// directories it removed. The rest of the file, comments and layout
// included, is left byte for byte. The edit is checked by parsing: if the
// result differs from the original by anything but those tables, nothing is
// written. A missing file has nothing to forget.
//
// Codex may rewrite the file meanwhile (a running TUI saving a setting), so
// the file is replaced only if it is still what was read, retrying
// otherwise. That narrows the window to the rename; it can't close it, as
// Codex takes no lock on config.toml.
func (f *TrustFile) Forget(dirs ...string) ([]string, error) {
	for range forgetAttempts {
		orig, err := os.ReadFile(f.Path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		edited, removed := removeProjects(orig, dirs)
		if len(removed) == 0 {
			return nil, nil
		}
		if err := checkOnlyRemoved(orig, edited, removed); err != nil {
			return nil, fmt.Errorf("codex: leaving %s alone: %w", f.Path, err)
		}
		replaced, err := f.replaceIfUnchanged(orig, edited)
		if err != nil {
			return nil, err
		}
		if replaced {
			return removed, nil
		}
	}
	return nil, fmt.Errorf("codex: %s kept changing while trust entries were removed", f.Path)
}

func (f *TrustFile) replaceIfUnchanged(orig, edited []byte) (bool, error) {
	info, err := os.Stat(f.Path)
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".config.toml.slop-bot-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := writeAll(tmp, edited, info.Mode().Perm()); err != nil {
		return false, err
	}
	if f.beforeCheck != nil {
		f.beforeCheck()
	}
	now, err := os.ReadFile(f.Path)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(now, orig) {
		return false, nil
	}
	return true, os.Rename(tmp.Name(), f.Path)
}

// writeAll writes b to tmp with mode, durably, and closes it.
func writeAll(tmp *os.File, b []byte, mode os.FileMode) error {
	err := tmp.Chmod(mode)
	if err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	return errors.Join(err, tmp.Close())
}

// projectHeader is a `[projects.<key>]` table header, as Codex writes them.
var projectHeader = regexp.MustCompile(`^\s*\[\s*projects\s*\.\s*("(?:[^"\\]|\\.)*"|'[^']*'|[A-Za-z0-9_-]+)\s*\]\s*(?:#.*)?$`)

// removeProjects cuts the tables of dirs out of src. A table runs from its
// header to the next header; comment lines just before the next header are
// left, as they likely describe it.
func removeProjects(src []byte, dirs []string) ([]byte, []string) {
	lines := strings.SplitAfter(string(src), "\n")
	var out strings.Builder
	var removed []string
	for i := 0; i < len(lines); {
		dir, ok := projectKey(lines[i])
		if !ok || !slices.Contains(dirs, dir) {
			out.WriteString(lines[i])
			i++
			continue
		}
		end := i + 1
		for end < len(lines) && !isHeader(lines[end]) {
			end++
		}
		keep := end
		for keep > i+1 && strings.HasPrefix(strings.TrimSpace(lines[keep-1]), "#") {
			keep--
		}
		for _, l := range lines[keep:end] {
			out.WriteString(l)
		}
		removed = append(removed, dir)
		i = end
	}
	return []byte(out.String()), removed
}

func isHeader(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "[")
}

// projectKey is the directory a `[projects."<dir>"]` header names.
func projectKey(line string) (string, bool) {
	m := projectHeader.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
	if m == nil {
		return "", false
	}
	key := m[1]
	if key[0] != '"' && key[0] != '\'' {
		return key, true
	}
	// Let the TOML parser unescape the quoted key.
	var v struct{ K string }
	if _, err := toml.Decode("K = "+key, &v); err != nil {
		return "", false
	}
	return v.K, true
}

// checkOnlyRemoved parses both versions and requires them to be equal but
// for the removed projects.
func checkOnlyRemoved(orig, edited []byte, removed []string) error {
	var before, after map[string]any
	if _, err := toml.Decode(string(orig), &before); err != nil {
		return fmt.Errorf("it doesn't parse: %w", err)
	}
	if _, err := toml.Decode(string(edited), &after); err != nil {
		return fmt.Errorf("the edit wouldn't parse: %w", err)
	}
	if projects, ok := before["projects"].(map[string]any); ok {
		for _, d := range removed {
			delete(projects, d)
		}
		if _, still := after["projects"]; len(projects) == 0 && !still {
			delete(before, "projects")
		}
	}
	if !reflect.DeepEqual(before, after) {
		return errors.New("the edit would change more than the trust entries")
	}
	return nil
}
