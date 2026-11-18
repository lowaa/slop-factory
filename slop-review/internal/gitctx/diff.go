// Package gitctx resolves what slop-review reviews (the diff scope) and
// exposes the read-only git plumbing (show/log/blame) the review session's
// toolset dispatches into for extra context. All operations are read-only:
// nothing here ever mutates the repo.
package gitctx

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/lowaa/slop-factory/slop-review/internal/secrets"
)

// mainBranchCandidates is tried in order to find the branch to diff against.
// Most repos here use "main"; this also covers the rare "master" holdout.
var mainBranchCandidates = []string{"main", "origin/main", "master", "origin/master"}

// Scope is the resolved diff slop-review reviews: everything
// since BaseRef, both committed (BaseRef..HEAD) and uncommitted (working
// tree), in one unified diff -- `git diff <BaseRef>` covers both in a
// single invocation, since a one-sided `git diff <ref>` compares that ref's
// tree against the working tree (index and worktree together), not just
// HEAD.
type Scope struct {
	Branch  string
	BaseRef string
	// Files is the diff split per file, in git's order, with secret-shaped
	// files already redacted.
	Files []FileDiff
	// HeadSHA and Dirty describe the checkout at the moment the diff was
	// taken -- recorded here, beside it, so a report can't pair this diff
	// with a HEAD that moved while the review ran.
	HeadSHA string
	Dirty   bool
	// RemoteURL is the URL configured for the remote named by
	// ScopeOptions.RemoteName ("origin" by default), or "" if the repo has
	// no such remote -- see RemoteURL's own doc comment for why that's not
	// an error here. Only internal/runreport, building a report, requires
	// this to be non-empty.
	RemoteURL string
	// HeadAuthor is HEAD's commit author, "Name <email>" -- see the
	// package-level HeadAuthor's doc comment.
	HeadAuthor string
	// RepoRoot is the git repository's toplevel, which is NOT necessarily
	// the directory ResolveScope was pointed at: every path in Diff is
	// relative to the toplevel (that's what git emits, from any cwd), so
	// the review session's toolset has to be rooted here too, or the model
	// can't open the very files the diff names.
	RepoRoot string
	// ScopePath is the toplevel-relative subdirectory the review was
	// narrowed to ("" when reviewing the whole repo) -- i.e. what the
	// caller pointed ResolveScope at, expressed the same way Diff's paths
	// are.
	ScopePath string
	// Warnings records any secret-shaped file whose diff was included
	// anyway because allowSecrets was set (see ResolveScope) -- merged into
	// the final Review's own Warnings by cmd/slop-review.
	Warnings []string
	// CommitLog is the branch's commit messages from BaseRef to HEAD,
	// oldest first, merge commits dropped and capped at CommitLogMaxBytes.
	// It is context, not part of the diff, so a log git can't read leaves
	// it empty and adds a warning rather than failing the review.
	CommitLog string
}

// ScopeOptions configures ResolveScope.
type ScopeOptions struct {
	// FromCommitHash, if set, reviews from this commit onwards instead of
	// the current branch's merge-base with main. Required when on main.
	FromCommitHash string
	// AllowSecrets mirrors internal/toolset's own flag of the same name:
	// by default, any file in the diff whose path matches a secret-shaped
	// pattern (see internal/secrets) has its content redacted before the
	// diff ever reaches the model -- the diff is the one thing every
	// review sees unconditionally, unlike the opt-in navigation tools, so
	// it needs the same protection they get.
	AllowSecrets bool
	// RemoteName is which git remote identifies this repository (see
	// report.RepoRemote/RemoteKey) -- "origin" if unset.
	RemoteName string
}

// ResolveScope determines what to review in repoRoot; see ScopeOptions for
// the knobs. If FromCommitHash is unset, the current branch is diffed
// against the merge-base with main -- unless the current branch *is* main,
// in which case there's no sensible default range and the caller must
// supply FromCommitHash.
func ResolveScope(repoRoot string, opts ScopeOptions) (Scope, error) {
	// Everything below runs against the repository toplevel rather than
	// repoRoot itself, with repoRoot's own subdirectory applied as a
	// pathspec instead. `git -C <subdir> diff` is not scoped to that
	// subdir anyway (it reports the whole repo), and it emits
	// toplevel-relative paths regardless -- while `git ls-files --others`
	// run there emits subdir-relative ones. Normalising to the toplevel is
	// what keeps the two halves of the diff, and the toolset the model
	// navigates with, all speaking the same paths.
	toplevel, scopePath, err := repoLocation(repoRoot)
	if err != nil {
		return Scope{}, err
	}

	branch, err := currentBranch(toplevel)
	if err != nil {
		return Scope{}, err
	}

	baseRef := opts.FromCommitHash
	if baseRef == "" {
		if branch == "main" || branch == "master" {
			return Scope{}, fmt.Errorf("gitctx: on branch %q, which can't be reviewed against itself -- pass --from-commit-hash", branch)
		}
		baseRef, err = mergeBaseWithMain(toplevel)
		if err != nil {
			return Scope{}, err
		}
	} else {
		if err := validateRevisionArg(baseRef); err != nil {
			return Scope{}, fmt.Errorf("gitctx: --from-commit-hash: %w", err)
		}
		if _, err := runGit(toplevel, "rev-parse", "--verify", baseRef+"^{commit}"); err != nil {
			return Scope{}, fmt.Errorf("gitctx: --from-commit-hash %q does not resolve to a commit: %w", opts.FromCommitHash, err)
		}
	}

	// --find-renames is explicit rather than left to the target repo's own
	// diff.renames config: a rename needs to actually render as one (with
	// "--- a/<old>"/"+++ b/<new>" lines) for diffBlockSecretPath below to
	// see both paths -- relying on ambient config would make that
	// redaction path exercise or not depending on a setting outside
	// slop-review's control.
	diff, err := runGit(toplevel, withPathspec([]string{"diff", "--find-renames", baseRef}, scopePath)...)
	if err != nil {
		return Scope{}, fmt.Errorf("gitctx: diff against %s: %w", baseRef, err)
	}

	// `git diff` never includes untracked files (only tracked changes,
	// staged or not) -- fold those in too, since a new file is exactly the
	// kind of uncommitted-WIP change a reviewer needs to see.
	untracked, err := untrackedFilesDiff(toplevel, scopePath)
	if err != nil {
		return Scope{}, err
	}
	diff += untracked

	files, warnings := splitFileDiffs(diff, opts.AllowSecrets)
	if len(files) == 0 {
		if scopePath != "" {
			return Scope{}, fmt.Errorf("gitctx: no changes under %s between %s and the working tree", scopePath, baseRef)
		}
		return Scope{}, fmt.Errorf("gitctx: no changes between %s and the working tree", baseRef)
	}
	if err := markGenerated(toplevel, baseRef, scopePath, files); err != nil {
		return Scope{}, fmt.Errorf("gitctx: detect generated files: %w", err)
	}

	headSHA, err := HeadSHA(toplevel)
	if err != nil {
		return Scope{}, err
	}
	dirty, err := Dirty(toplevel, scopePath)
	if err != nil {
		return Scope{}, err
	}

	remoteName := opts.RemoteName
	if remoteName == "" {
		remoteName = "origin"
	}
	remoteURL, err := RemoteURL(toplevel, remoteName)
	if err != nil {
		return Scope{}, err
	}
	headAuthor, err := HeadAuthor(toplevel)
	if err != nil {
		return Scope{}, err
	}

	commitLog, logWarning := CommitLog(toplevel, baseRef, CommitLogMaxBytes)
	if logWarning != "" {
		warnings = append(warnings, logWarning)
	}

	return Scope{
		Branch:     branch,
		BaseRef:    baseRef,
		Files:      files,
		HeadSHA:    headSHA,
		Dirty:      dirty,
		RemoteURL:  remoteURL,
		HeadAuthor: headAuthor,
		RepoRoot:   toplevel,
		ScopePath:  scopePath,
		Warnings:   warnings,
		CommitLog:  commitLog,
	}, nil
}

// Diff is the whole scope as one unified diff, already redacted.
func (s *Scope) Diff() string {
	var b strings.Builder
	for i := range s.Files {
		b.WriteString(s.Files[i].Block)
	}
	return b.String()
}

// repoLocation resolves dir to the git repository toplevel that contains it
// plus dir's own path relative to that toplevel ("" when dir is the
// toplevel, otherwise slash-terminated, as `git rev-parse --show-prefix`
// reports it).
func repoLocation(dir string) (toplevel, scopePath string, err error) {
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("gitctx: locate repository root: %w", err)
	}
	toplevel = strings.TrimSpace(out)

	out, err = runGit(dir, "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", fmt.Errorf("gitctx: locate path within repository: %w", err)
	}
	return toplevel, strings.TrimSpace(out), nil
}

// withPathspec narrows a git command to scopePath, so a review pointed at a
// subdirectory covers only that subtree while still reporting every path
// relative to the repository toplevel.
//
// The ":(top,literal)" magic matters: a pathspec is not literal by default,
// so a directory whose name starts with ":" would be parsed as pathspec
// magic rather than a path, and silently match nothing -- turning a review
// of that directory into "no changes". "literal" also takes wildcard
// characters out of play, and "top" states outright that scopePath is
// relative to the repository toplevel, which is where these commands run.
func withPathspec(args []string, scopePath string) []string {
	if scopePath == "" {
		return args
	}
	return append(args, "--", ":(top,literal)"+scopePath)
}

// diffOldPathRe and diffNewPathRe capture a unified diff block's "--- "
// (pre-change) and "+++ " (post-change) header line's remainder -- not the
// path itself yet. The remainder is either a bare "a/<path>"/"b/<path>"
// (common case) or, if git quoted the whole path (see gitUnquotePath), a
// double-quoted, C-escaped '"a/<path>"'/'"b/<path>"' string -- crucially,
// a quoted path wraps the a/ or b/ prefix too (`+++ "b/weird\\dir/.env"`,
// not `+++ "weird\\dir/.env"` after an unquoted "b/"), so the prefix can't
// be matched as literal regex text the way it could when nothing needs
// escaping. extractDiffPath below does that unwrapping.
//
// Kept as two separate patterns, rather than one alternation, because a
// renamed file has both, with two different paths -- and which one is
// checked matters: a rename can expose old secret content in the hunk
// under a new, non-secret-shaped name, or vice versa, so
// diffBlockSecretPath checks both rather than whichever happens to appear
// first in the text.
var (
	diffOldPathRe = regexp.MustCompile(`(?m)^--- (.+)$`)
	diffNewPathRe = regexp.MustCompile(`(?m)^\+\+\+ (.+)$`)
)

// extractDiffPath applies re to block and strips prefix ("a/" or "b/")
// from the captured remainder, unquoting it first if git quoted it. Returns
// "" if re doesn't match, or if the remainder doesn't have prefix (e.g.
// "/dev/null", for a new/deleted file's other side).
func extractDiffPath(re *regexp.Regexp, block, prefix string) string {
	m := re.FindStringSubmatch(block)
	if m == nil {
		return ""
	}
	spec := m[1]
	if len(spec) >= 2 && spec[0] == '"' && spec[len(spec)-1] == '"' {
		spec = gitUnquotePath(spec)
	}
	if !strings.HasPrefix(spec, prefix) {
		return ""
	}
	return spec[len(prefix):]
}

// diffBlockSecretPath reports whether block's pre- or post-change path
// looks like a secret, and returns whichever one does (preferring the
// post-change path if both do).
func diffBlockSecretPath(block string) (path string, isSecret bool) {
	oldPath := extractDiffPath(diffOldPathRe, block, "a/")
	newPath := extractDiffPath(diffNewPathRe, block, "b/")
	if newPath != "" && secrets.LooksLikeSecret(newPath) {
		return newPath, true
	}
	if oldPath != "" && secrets.LooksLikeSecret(oldPath) {
		return oldPath, true
	}
	return "", false
}

// gitUnquotePath undoes git's C-style path quoting. Every git invocation in
// this package sets core.quotepath=false (see runGitAllowExitCode), which
// covers the common case (non-ASCII bytes), but a path containing a
// backslash, double quote, or control character is quoted unconditionally
// regardless of that setting -- so a secret-shaped filename with one of
// those in it (or in a containing directory) would otherwise reach
// diffBlockSecretPath still wrapped in quotes and escaped, and fail to
// match. Returns path unchanged if it isn't quoted.
func gitUnquotePath(path string) string {
	if len(path) < 2 || path[0] != '"' || path[len(path)-1] != '"' {
		return path
	}
	inner := path[1 : len(path)-1]

	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c != '\\' || i+1 >= len(inner) {
			b.WriteByte(c)
			continue
		}
		i++
		switch inner[i] {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '\\', '"':
			b.WriteByte(inner[i])
		default:
			if i+2 < len(inner) && isOctalDigit(inner[i]) && isOctalDigit(inner[i+1]) && isOctalDigit(inner[i+2]) {
				b.WriteByte((inner[i]-'0')<<6 | (inner[i+1]-'0')<<3 | (inner[i+2] - '0'))
				i += 2
			} else {
				b.WriteByte(inner[i])
			}
		}
	}
	return b.String()
}

func isOctalDigit(c byte) bool {
	return c >= '0' && c <= '7'
}

// RedactSecretDiff splits diff -- a unified diff as `git diff`, `git diff
// --no-index`, or `git show <commit>` produces -- into its per-file
// "diff --git" blocks, and replaces the body of any block whose path looks
// like a secret (per internal/secrets) with a placeholder, unless
// allowSecrets is set -- in which case the block is left intact and its
// path is returned as a warning instead. Any preamble before the first
// "diff --git" line (e.g. `git show`'s commit header/message) is always
// kept as-is, never treated as part of the first file's block.
//
// Exported for internal/toolset's git_show tool, which needs the same
// redaction ResolveScope applies to the initial diff -- `git show <commit>`
// can otherwise surface a secret file's full historical content the same
// way the initial diff could before this existed.
func RedactSecretDiff(diff string, allowSecrets bool) (string, []string) {
	if diff == "" {
		return diff, nil
	}

	preamble, blocks := splitDiffPreambleAndBlocks(diff)
	var out strings.Builder
	out.WriteString(preamble)
	var warnings []string
	for _, block := range blocks {
		redacted, _, warning := redactBlock(block, allowSecrets)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		out.WriteString(redacted)
	}
	return out.String(), warnings
}

// redactBlock applies RedactSecretDiff's rule to one "diff --git" block,
// returning the block to show, the secret-shaped path if it matched one,
// and a warning if --allow-secrets let it through.
func redactBlock(block string, allowSecrets bool) (string, string, string) {
	secretPath, isSecret := diffBlockSecretPath(block)
	if !isSecret {
		return block, "", ""
	}
	if allowSecrets {
		return block, secretPath, fmt.Sprintf("included the diff for %q despite it matching a secret-shaped filename pattern (--allow-secrets was set)", secretPath)
	}
	return fmt.Sprintf("diff --git a/%s b/%s\n[omitted: %q matches a secret-shaped filename pattern; rerun with --allow-secrets to include it]\n", secretPath, secretPath, secretPath), secretPath, ""
}

// splitDiffPreambleAndBlocks splits diff into whatever precedes the first
// "diff --git" line (kept whole, e.g. `git show`'s commit header) and the
// per-file blocks from there on, each starting at its own "diff --git"
// line.
func splitDiffPreambleAndBlocks(diff string) (string, []string) {
	lines := strings.Split(diff, "\n")

	i := 0
	for ; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "diff --git ") {
			break
		}
	}
	preamble := ""
	if i > 0 {
		preamble = strings.Join(lines[:i], "\n") + "\n"
	}

	var blocks []string
	var current []string
	flush := func() {
		if len(current) > 0 {
			blocks = append(blocks, strings.Join(current, "\n")+"\n")
			current = nil
		}
	}
	for _, line := range lines[i:] {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
		}
		current = append(current, line)
	}
	flush()

	return preamble, blocks
}

// untrackedFilesDiff renders every untracked-but-not-ignored file as a
// unified "new file" diff, the same shape `git diff` produces for a tracked
// addition. `git diff --no-index` is read-only (unlike `git add -N`, which
// would also make untracked files visible to a plain `git diff` but does so
// by writing intent-to-add entries into the index).
func untrackedFilesDiff(repoRoot, scopePath string) (string, error) {
	out, err := runGit(repoRoot, withPathspec([]string{"ls-files", "--others", "--exclude-standard"}, scopePath)...)
	if err != nil {
		return "", fmt.Errorf("gitctx: list untracked files: %w", err)
	}

	var b strings.Builder
	for _, f := range strings.Split(out, "\n") {
		if f == "" {
			continue
		}
		// `git ls-files` quotes a path the same way diff headers do (see
		// gitUnquotePath) -- without unquoting first, a path needing it
		// would be passed to `git diff --no-index` below as a literal
		// (bogus) filesystem path, which fails but still exits 1 (the same
		// code that means "differences found" here), so the failure would
		// otherwise go unnoticed and the file would just silently vanish
		// from the review instead of erroring or appearing.
		f = gitUnquotePath(f)
		// git diff --no-index uses exit-code semantics unconditionally
		// (unlike a plain `git diff <ref>`): 1 means "differences found",
		// which is the expected/only outcome here, not a failure.
		d, err := runGitAllowExitCode(repoRoot, 1, "diff", "--no-index", "--no-color", "--", "/dev/null", f)
		if err != nil {
			return "", fmt.Errorf("gitctx: diff untracked file %q: %w", f, err)
		}
		b.WriteString(d)
	}
	return b.String(), nil
}

func currentBranch(repoRoot string) (string, error) {
	out, err := runGit(repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitctx: determine current branch: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// mergeBaseWithMain is the merge-base nearest HEAD among the main branch
// candidates that exist. Taking the first that exists would let a stale local
// main win over a fresh origin/main: a branch cut from origin/main would then
// be diffed against an old commit, and the review would cover everything
// merged in between.
func mergeBaseWithMain(repoRoot string) (string, error) {
	var bases []string
	var lastErr error
	for _, candidate := range mainBranchCandidates {
		out, err := runGit(repoRoot, "merge-base", candidate, "HEAD")
		if err != nil {
			lastErr = err
			continue
		}
		bases = append(bases, strings.TrimSpace(out))
	}
	if len(bases) == 0 {
		return "", fmt.Errorf("gitctx: no main/master branch found to compute a merge-base against (tried %v): %w", mainBranchCandidates, lastErr)
	}
	// The nearest is the one with the fewest commits between it and HEAD,
	// which is the smallest review. When one base is an ancestor of another
	// that is simply the later one; it also decides between bases neither of
	// which is an ancestor of the other, as when the branch merged both.
	nearest, fewest := "", -1
	for _, base := range bases {
		out, err := runGit(repoRoot, "rev-list", "--count", base+"..HEAD")
		if err != nil {
			return "", err
		}
		n, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			return "", fmt.Errorf("gitctx: count commits since %s: %w", base, err)
		}
		if fewest < 0 || n < fewest {
			nearest, fewest = base, n
		}
	}
	return nearest, nil
}

// runGit invokes git with a fixed argv (never a shell-interpolated command
// string), scoped to repoRoot via -C.
func runGit(repoRoot string, args ...string) (string, error) {
	return runGitAllowExitCode(repoRoot, -1, args...)
}

// runGitAllowExitCode is runGit, but additionally treats okExitCode as
// success (on top of 0) -- see untrackedFilesDiff's use of it for why.
//
// core.quotepath=false is set on every invocation so a path containing
// non-ASCII bytes renders literally rather than C-quoted -- reduces how
// often gitUnquotePath's fallback is needed, though it isn't a complete
// substitute (a backslash/quote/control character is always quoted
// regardless of this setting).
func runGitAllowExitCode(repoRoot string, okExitCode int, args ...string) (string, error) {
	return runGitCommand(repoRoot, okExitCode, "", args...)
}

// runGitWithStdin is runGit, feeding stdin to the command.
func runGitWithStdin(repoRoot, stdin string, args ...string) (string, error) {
	return runGitCommand(repoRoot, -1, stdin, args...)
}

func runGitCommand(repoRoot string, okExitCode int, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "core.quotepath=false", "-C", repoRoot}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if okExitCode >= 0 && errors.As(err, &exitErr) && exitErr.ExitCode() == okExitCode {
			return stdout.String(), nil
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// validateArg rejects a value that could be misread as a flag by git
// (leading "-") or that carries whitespace/control characters -- defense in
// depth against argument injection via a model- or caller-supplied value,
// on top of always passing arguments as their own argv entry rather than
// through a shell. label names the argument in the error (e.g. "revision",
// "remote name").
func validateArg(label, value string) error {
	if value == "" {
		return fmt.Errorf("empty %s", label)
	}
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s %q looks like a flag", label, value)
	}
	for _, r := range value {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("%s %q contains whitespace/control characters", label, value)
		}
	}
	return nil
}

// validateRevisionArg is validateArg for a git revision (a ref, a SHA, a
// range).
func validateRevisionArg(rev string) error {
	return validateArg("revision", rev)
}
