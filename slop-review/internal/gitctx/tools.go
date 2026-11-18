package gitctx

import (
	"fmt"
	"strconv"
	"strings"
)

// HeadSHA returns the full commit hash HEAD currently points to -- what a
// posted report (see internal/runreport) records as the run's head_sha.
func HeadSHA(repoRoot string) (string, error) {
	out, err := runGit(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitctx: head sha: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// Dirty reports whether the working tree has uncommitted or untracked
// changes under scopePath ("" for the whole repository): staged or
// unstaged edits to tracked files, plus untracked files `git status`
// itself would report (already excludes .gitignore'd paths). Read-only,
// like everything else in this package -- `git status --porcelain`
// inspects the index and working tree without writing to either.
func Dirty(repoRoot, scopePath string) (bool, error) {
	out, err := runGit(repoRoot, withPathspec([]string{"status", "--porcelain"}, scopePath)...)
	if err != nil {
		return false, fmt.Errorf("gitctx: dirty: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}

// RemoteURL returns the URL git has configured for remoteName ("origin" is
// the usual case), or "" if the repo has no such remote -- not an error
// here: ResolveScope must still work for a repo with no remote configured
// (a bare local clone, a checkout that was never pushed anywhere), and only
// reporting actually needs the URL. Read-only: `git remote get-url` only
// reads config, same as everything else in this package.
func RemoteURL(repoRoot, remoteName string) (string, error) {
	if err := validateArg("remote name", remoteName); err != nil {
		return "", fmt.Errorf("gitctx: remote url: %w", err)
	}
	out, err := runGit(repoRoot, "remote", "get-url", remoteName)
	if err != nil {
		if isNoSuchRemote(err) {
			return "", nil
		}
		return "", fmt.Errorf("gitctx: remote url: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// isNoSuchRemote reports whether err is `git remote get-url`'s failure mode
// for a remote name that isn't configured -- distinguished from any other
// failure (not a git repo, git itself missing) that RemoteURL should still
// surface as an error rather than quietly return "".
func isNoSuchRemote(err error) bool {
	return strings.Contains(err.Error(), "No such remote")
}

// HeadAuthor returns HEAD's commit author, as git records it: "Name
// <email>". What a posted report (see internal/runreport) records as the
// run's head_author -- informational only, since it names whoever the
// commit's author trailer says, not whoever ran slop-review.
func HeadAuthor(repoRoot string) (string, error) {
	out, err := runGit(repoRoot, "log", "-1", "--format=%an <%ae>", "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitctx: head author: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// Show returns `git show` for ref -- a commit, tag, or <ref>:<path> blob
// reference -- so the review session can inspect a specific commit or an
// older version of a file.
func Show(repoRoot, ref string) (string, error) {
	if err := validateRevisionArg(ref); err != nil {
		return "", fmt.Errorf("gitctx: show: %w", err)
	}
	// --find-renames for the same reason ResolveScope's own `git diff` uses
	// it: a commit-ish ref's output goes through RedactSecretDiff, which
	// needs a detected rename to actually render as one to see both paths.
	return runGit(repoRoot, "show", "--no-color", "--find-renames", ref)
}

// Log returns `git log --oneline` over revisionRange (e.g. "base..HEAD", a
// single ref, or "" for HEAD), capped at maxCount entries, optionally
// scoped to path.
func Log(repoRoot, revisionRange string, maxCount int, path string) (string, error) {
	args := []string{"log", "--no-color", "--oneline", "-n", strconv.Itoa(maxCount)}
	if revisionRange != "" {
		if err := validateRevisionArg(revisionRange); err != nil {
			return "", fmt.Errorf("gitctx: log: %w", err)
		}
		args = append(args, revisionRange)
	}
	if path != "" {
		args = append(args, "--", path)
	}
	return runGit(repoRoot, args...)
}

// Blame returns `git blame` for path, optionally restricted to
// [startLine, endLine] (both 1-based, inclusive; either may be 0 to mean
// "unbounded on that side").
func Blame(repoRoot, path string, startLine, endLine int) (string, error) {
	// No --no-color: unlike show/log, git blame doesn't recognize it (and
	// doesn't colorize non-tty output by default anyway).
	args := []string{"blame"}
	if startLine > 0 || endLine > 0 {
		args = append(args, "-L", blameRange(startLine, endLine))
	}
	args = append(args, "--", path)
	return runGit(repoRoot, args...)
}

func blameRange(start, end int) string {
	startStr, endStr := "", ""
	if start > 0 {
		startStr = strconv.Itoa(start)
	}
	if end > 0 {
		endStr = strconv.Itoa(end)
	}
	return startStr + "," + endStr
}
