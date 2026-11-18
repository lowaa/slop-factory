package bench

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BuildRepo creates, under dir, a fresh repository holding just the
// case's change: Base's tree committed on main, then Head's tree plus the
// planted bugs committed on a feature branch.
//
// A fresh repository rather than a clone, because a clone carries the real
// history: the reviewer has git_log and git_show, and could find the
// original, correct version of every line the patch broke.
func BuildRepo(sourceRepo string, c *Case, dir string) (string, error) {
	repo := filepath.Join(dir, "repo")
	// Refuse rather than reuse: a repository left by an earlier or
	// interrupted run would carry its history and files into this one,
	// and the benchmark would silently score a different change.
	if _, err := os.Stat(repo); err == nil {
		return "", fmt.Errorf("bench: %s already exists; use a fresh --work-dir", repo)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("bench: check repo dir: %w", err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return "", fmt.Errorf("bench: create repo dir: %w", err)
	}
	b := &repoBuilder{source: sourceRepo, repo: repo, scratch: dir}

	// --force on every add: everything extracted is tracked in the source
	// repository by definition, including files its .gitignore matches.
	b.git("init", "--quiet", "--initial-branch=main")
	b.extract(c.Base)
	b.git("add", "--all", "--force")
	b.git("commit", "--quiet", "--no-verify", "-m", "base")

	b.git("checkout", "--quiet", "-b", "feature")
	b.git("rm", "-r", "--quiet", "--cached", ".")
	b.clearWorktree()
	b.extract(c.Head)
	if patch := c.PatchPath(); patch != "" {
		b.git("apply", "--whitespace=nowarn", patch)
	}
	b.git("add", "--all", "--force")
	message := c.CommitMessage
	if message == "" {
		message = "change"
	}
	b.git("commit", "--quiet", "--no-verify", "-m", message)

	if b.err != nil {
		return "", b.err
	}
	return repo, nil
}

// repoBuilder runs a fixed sequence of git steps, stopping at the first
// failure so BuildRepo reads as the sequence rather than as error checks.
type repoBuilder struct {
	source, repo, scratch string
	err                   error
}

func (b *repoBuilder) git(args ...string) {
	b.run(b.repo, "git", args...)
}

func (b *repoBuilder) run(dir, name string, args ...string) {
	if b.err != nil {
		return
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// A fixed identity, and no user or system config: hooks, signing and
	// templates from the host have no business in a throwaway repo.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=bench", "GIT_AUTHOR_EMAIL=bench@example.invalid",
		"GIT_COMMITTER_NAME=bench", "GIT_COMMITTER_EMAIL=bench@example.invalid",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		b.err = fmt.Errorf("bench: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
}

// extract writes ref's tree from the source repository into the new one.
func (b *repoBuilder) extract(ref string) {
	archive := filepath.Join(b.scratch, "tree.tar")
	b.run(b.source, "git", "archive", "--format=tar", "-o", archive, ref)
	b.run(b.repo, "tar", "-xf", archive)
	if b.err == nil {
		if err := os.Remove(archive); err != nil {
			b.err = fmt.Errorf("bench: remove archive: %w", err)
		}
	}
}

// clearWorktree empties the repository's working tree (but not .git), so
// files deleted between Base and Head don't survive into Head.
func (b *repoBuilder) clearWorktree() {
	if b.err != nil {
		return
	}
	entries, err := os.ReadDir(b.repo)
	if err != nil {
		b.err = fmt.Errorf("bench: read worktree: %w", err)
		return
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(b.repo, e.Name())); err != nil {
			b.err = fmt.Errorf("bench: clear worktree: %w", err)
			return
		}
	}
}
