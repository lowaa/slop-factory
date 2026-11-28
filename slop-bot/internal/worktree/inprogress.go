package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// inProgressMarkers are what git leaves in a worktree's admin dir while a
// rebase, am, merge, cherry-pick or revert is stopped part way. With
// --autostash (or rebase.autoStash), the uncommitted edits are held there too,
// not in the working tree, so `git status` can be clean while removing the
// worktree would lose them.
var inProgressMarkers = []string{
	"rebase-merge", "rebase-apply", "MERGE_HEAD", "MERGE_AUTOSTASH", "CHERRY_PICK_HEAD", "REVERT_HEAD",
}

// autostashFiles each hold the id of a stash commit git will re-apply when the
// operation finishes.
var autostashFiles = []string{"rebase-merge/autostash", "rebase-apply/autostash", "MERGE_AUTOSTASH"}

// adminDir is the worktree at path's admin dir in repoPath's common git dir,
// found through each admin dir's gitdir file, so it works whether or not the
// checkout is present. Empty when there is none.
func (m *Manager) adminDir(ctx context.Context, repoPath, path string) (string, error) {
	common, err := m.commonDir(ctx, repoPath)
	if err != nil {
		return "", err
	}
	root := filepath.Join(common, "worktrees")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	want := filepath.Join(path, ".git")
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "gitdir")) // #nosec G304 -- git's own admin file
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		// With worktree.useRelativePaths the path is relative to the admin
		// dir; unresolved, the worktree goes unrecognised and its autostash
		// unprotected.
		gitdir := strings.TrimSpace(string(raw))
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(dir, gitdir)
		}
		if canon(gitdir) == canon(want) {
			return dir, nil
		}
	}
	return "", nil
}

// inProgress names the stopped operations in adminDir.
func inProgress(adminDir string) ([]string, error) {
	var found []string
	for _, name := range inProgressMarkers {
		if _, err := os.Lstat(filepath.Join(adminDir, name)); err == nil {
			found = append(found, name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return found, nil
}

// saveAutostashes puts any autostash held in the worktree at path's admin dir
// into the repo's stash list, where it outlives the admin dir: recover is
// about to drop that. It writes refs/stash and its reflog as `git stash
// store` does, because that command needs a work tree, which a bare repo in
// the lookup hasn't; any of its worktrees lists the entry, since they share
// its refs.
func (m *Manager) saveAutostashes(ctx context.Context, repoPath, path string) error {
	dir, err := m.adminDir(ctx, repoPath, path)
	if err != nil || dir == "" {
		return err
	}
	for _, name := range autostashFiles {
		raw, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- git's own admin file
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		oid := strings.TrimSpace(string(raw))
		msg := fmt.Sprintf("slop-bot: autostash from %s, deleted mid-operation", path)
		if _, err := m.git.Run(ctx, repoPath, "update-ref", "--create-reflog", "-m", msg, "refs/stash", oid); err != nil {
			return fmt.Errorf("worktree: saving the autostash in %s: %w", path, err)
		}
		m.logger.Warn("saved a deleted worktree's autostash to the stash list", "path", path, "stash", oid)
	}
	return nil
}
