// Package manifest is a tenant's declaration of what its workspace looks
// like -- which repo is checked out at which path under the root -- and the
// rules that follow from it: whether an agent's reported layout matches,
// and what to propose when the tenant has declared nothing yet.
//
// A tenant with no manifest is "not declared", not "empty": nothing here is
// enforced for it, and nothing is ever inferred into one. Only an owner
// adopts a manifest.
package manifest

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// Repo is one manifest entry. Remote is stored normalised (NormaliseRemote),
// which is also the form every comparison uses.
type Repo struct {
	Path   string `json:"path" doc:"Where the repo is checked out, relative to the workspace root: one directory name." example:"app"`
	Remote string `json:"remote" doc:"The repo's origin remote, e.g. github.com/acme/app. Any scheme, user, port or .git suffix is dropped." example:"github.com/acme/app"`
	// BaseBranch is where this repo's tickets merge for the tenant. Owner-only
	// like the rest: an agent that could would choose where its own work lands.
	BaseBranch string `json:"base_branch,omitempty" maxLength:"255" doc:"The branch this tenant's changes to the repo merge into, e.g. slop-haus-acme. Empty means the repo's default branch. Created from the default branch's tip when missing. Nothing reads it yet." example:"slop-haus-acme"`
	// DeployOnly loosens the merge gate, so only an owner sets it, like the
	// rest of the manifest: an agent that could would choose its own gate.
	DeployOnly []DeployOnlyRule `json:"deploy_only,omitempty" maxItems:"50" doc:"Files of this repo that only deploys change (e.g. image tags CI commits). A change whose head is behind the base branch only by commits to these files, and that does not touch them itself, may merge without merging the base again."`
	// SharedPaths loosens the scope guard, so it is owner-only for the same
	// reason: an agent that could would pick which of its own overlaps count.
	SharedPaths []SharedPathRule `json:"shared_paths,omitempty" maxItems:"50" doc:"Paths of this repo where tickets' scopes may overlap without blocking each other, because a clash there is mechanical (a migrations directory, generated output). An overlap lying wholly inside one never stops a claim or a scope extension; it is still reported, labelled shared, with how it is resolved."`
	// ProtectedPaths names what the steward may never judge alone. An agent
	// that could edit it would choose its own limits, so it is owner-only.
	ProtectedPaths []ProtectedPathRule `json:"protected_paths,omitempty" maxItems:"100" doc:"Paths of this repo that are always the owner's decision for the steward's judgment categories (approving reviews, picking discussion answers, opening tickets): a change or ticket touching one is never decided by the steward, at any autonomy level. Matched like shared_paths. Does not affect workers, scopes or merging. Nothing reads it yet."`
	// IndependentApps is a fact about the repo; the two switches below
	// loosen the merge gate (a change behind main may merge on its existing CI,
	// and several changes may land as one tested batch), so they are owner-only
	// like the rest.
	IndependentApps      []string `json:"independent_apps,omitempty" maxItems:"50" doc:"Apps: directories of this repo (e.g. slop-haus/) that build, test and deploy on their own, so a change inside one cannot break another's tests. Stored as directory prefixes with a trailing /. Used by: merge batching (batch_within_apps) and re-test skipping (skip_retest_across_apps). Empty means no app is declared."`
	BatchWithinApps      bool     `json:"batch_within_apps,omitempty" doc:"Batch merges within an app: with merge_queue_batch_size above 1, changes that each touch only one app (the same one) may land as a single tested batch. Needs independent_apps; changes touching files outside the apps land alone. Off by default."`
	SkipRetestAcrossApps bool     `json:"skip_retest_across_apps,omitempty" doc:"Skip re-testing when main moved only in other apps: a change behind main may merge on its existing CI when main moved only in other independent_apps (or deploy-only files). Needs independent_apps. Off by default."`
}

// SharedPathRule is one entry of a repo's shared_paths.
type SharedPathRule struct {
	Path    string `json:"path" maxLength:"512" doc:"Relative to the repo's own root: a directory prefix when it ends in /, otherwise a file pattern as Go's path.Match reads it (* and ? do not cross a /)." example:"server/internal/schema/migrations/"`
	Resolve string `json:"resolve" enum:"migration,regenerate,merge" doc:"How a clash there is resolved when main is merged: migration (renumber to follow what landed, update LATEST, check it still makes sense), regenerate (re-run the generator, never hand-merge) or merge (merge by hand, keeping both sides' intent)."`
}

// ProtectedPathRule is one entry of a repo's protected_paths.
type ProtectedPathRule struct {
	Path   string `json:"path" maxLength:"512" doc:"Relative to the repo's own root, matched like a shared path: a directory prefix when it ends in /, otherwise a file pattern as Go's path.Match reads it (* and ? do not cross a /)." example:"infrastructure/"`
	Reason string `json:"reason,omitempty" maxLength:"512" doc:"Optional note on why the path is protected, for whoever reads the manifest later."`
}

// DeployOnlyRule is one entry of a repo's deploy_only.
type DeployOnlyRule struct {
	Path  string `json:"path" maxLength:"512" doc:"A file pattern relative to the repo's own root, as Go's path.Match reads it: * and ? do not cross a /." example:"deploy/envs/*/kustomization.yaml"`
	Lines string `json:"lines,omitempty" maxLength:"512" doc:"Optional regular expression (RE2) that every line a deploy adds to or removes from a matching file must match in full; a commit changing any other line of it does not count as a deploy." example:"\\s*newTag: \\S+"`
}

// Limits generous for anything real: a workspace is a handful of repos.
const (
	MaxRepos  = agent.MaxLayoutRepos
	MaxRemote = agent.MaxLayoutOrigin
	// MaxBranch is a base branch's length in bytes.
	MaxBranch = 255
	// MaxDeployOnly is rules per repo; MaxDeployOnlyText bounds each
	// pattern and expression.
	MaxDeployOnly     = 50
	MaxDeployOnlyText = 512
	// MaxSharedPaths is rules per repo; their text is bounded like deploy-only's.
	MaxSharedPaths = 50
	// MaxProtectedPaths is rules per repo; MaxProtectedReason bounds a reason.
	MaxProtectedPaths  = 100
	MaxProtectedReason = 512
	// MaxIndependentApps is directories per repo.
	MaxIndependentApps = 50
)

var (
	ErrInvalid  = errors.New("manifest: invalid")
	ErrNotOwner = errors.New("manifest: only the tenant's owners can change it")
	// ErrStaleRevision is a save made against a revision that is no longer
	// the manifest's: it changed since the saver read it.
	ErrStaleRevision = errors.New("manifest: it has changed since that revision")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// NormaliseRemote reduces a git remote to what names the repository, so the
// same repo reached by different URLs compares equal: `git@github.com:a/b.git`,
// `https://github.com/a/b` and `ssh://git@GitHub.com:22/a/b/` all become
// `github.com/a/b`.
//
// Lower-cased throughout, not only the host: GitHub, the forge in use,
// ignores case in owner and repo, and wrongly matching two repos that differ
// only in case is far less harmful than stopping an agent over a remote
// spelled differently.
func NormaliseRemote(remote string) string {
	// The userinfo and scheme go first, so a token pasted into a remote can
	// never be stored.
	s := agent.StripUserinfo(remote)
	if host, rest, ok := strings.Cut(s, ":"); ok {
		switch {
		case strings.HasPrefix(rest, "/"):
			// Not a repo path on a forge: leave it be.
		case portThenSlash(rest):
			// A port is transport, not identity.
			_, path, _ := strings.Cut(rest, "/")
			s = host + "/" + path
		default:
			// scp-like: host:owner/repo.
			s = host + "/" + rest
		}
	}
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimRight(s, "/")
	return strings.ToLower(s)
}

func portThenSlash(s string) bool {
	digits, _, ok := strings.Cut(s, "/")
	if !ok || digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Normalise validates a manifest's shape as an owner submitted it and returns
// it in the form that is stored.
func Normalise(repos []Repo) ([]Repo, error) {
	if len(repos) == 0 {
		return nil, invalid("a manifest needs at least one repo; remove it to go back to not declaring one")
	}
	if len(repos) > MaxRepos {
		return nil, invalid("at most %d repos", MaxRepos)
	}
	out := make([]Repo, 0, len(repos))
	paths := map[string]bool{}
	for i, r := range repos {
		path := strings.TrimSpace(r.Path)
		if err := validPath(path); err != nil {
			return nil, invalid("repo %d: %v", i+1, err)
		}
		if paths[path] {
			return nil, invalid("repo %d: path %q is listed twice", i+1, path)
		}
		paths[path] = true

		remote := NormaliseRemote(r.Remote)
		switch {
		case remote == "":
			return nil, invalid("repo %d (%s): a remote is needed", i+1, path)
		case len([]rune(remote)) > MaxRemote:
			return nil, invalid("repo %d (%s): remote is longer than %d characters", i+1, path, MaxRemote)
		case hasControl(remote) || strings.ContainsAny(remote, " \t"):
			return nil, invalid("repo %d (%s): remote has characters a remote cannot", i+1, path)
		case !cloneableRemote.MatchString(remote) || hasDotSegment(remote):
			return nil, invalid("repo %d (%s): remote %q is not a repository, e.g. github.com/acme/app", i+1, path, remote)
		}
		rules, err := normaliseDeployOnly(r.DeployOnly)
		if err != nil {
			return nil, invalid("repo %d (%s): deploy_only: %v", i+1, path, err)
		}
		shared, err := normaliseSharedPaths(r.SharedPaths)
		if err != nil {
			return nil, invalid("repo %d (%s): shared_paths: %v", i+1, path, err)
		}
		protected, err := normaliseProtectedPaths(r.ProtectedPaths)
		if err != nil {
			return nil, invalid("repo %d (%s): protected_paths: %v", i+1, path, err)
		}
		apps, err := normaliseIndependentApps(r.IndependentApps)
		if err != nil {
			return nil, invalid("repo %d (%s): independent_apps: %v", i+1, path, err)
		}
		base, err := NormaliseBaseBranch(r.BaseBranch)
		if err != nil {
			return nil, invalid("repo %d (%s): base_branch: %v", i+1, path, err)
		}
		out = append(out, Repo{Path: path, Remote: remote, BaseBranch: base, DeployOnly: rules, SharedPaths: shared,
			ProtectedPaths: protected, IndependentApps: apps,
			BatchWithinApps: r.BatchWithinApps, SkipRetestAcrossApps: r.SkipRetestAcrossApps})
	}
	return out, nil
}

// NormaliseBaseBranch validates a base branch; empty is the repo's default
// branch and stays empty.
func NormaliseBaseBranch(b string) (string, error) {
	b = strings.TrimSpace(b)
	if b == "" {
		return "", nil
	}
	if err := validBranchName(b); err != nil {
		return "", err
	}
	// Git cannot hold a branch x and another x/y, so a base that is, or sits
	// under, a name slop-haus makes would stop it making that branch. Those
	// names use dashes only, so a prefix test covers every one of them.
	for _, prefix := range reservedBranchPrefixes {
		if strings.HasPrefix(b, prefix) {
			return "", fmt.Errorf("%q collides with the %s* branches slop-haus makes", b, prefix)
		}
	}
	return b, nil
}

// reservedBranchPrefixes are the branches slop-haus creates: attempt branches
// (ticket-<id>-a<n>) and unpromoted bases (slop-haus-base-ticket-<id>).
var reservedBranchPrefixes = []string{"ticket-", "slop-haus-base-ticket-"}

// validBranchName follows git check-ref-format --branch for a branch name
// the code host will take.
func validBranchName(b string) error {
	switch {
	case len(b) > MaxBranch:
		return fmt.Errorf("a branch name is at most %d bytes", MaxBranch)
	case b == "@" || b == "HEAD":
		return fmt.Errorf("%q is not a branch name", b)
	case hasControl(b) || strings.ContainsAny(b, " ~^:?*[\\\x7f"):
		return errors.New("a branch name cannot have spaces, control characters or any of ~ ^ : ? * [ \\")
	case strings.HasPrefix(b, "-"):
		return errors.New("a branch name cannot start with -")
	case strings.Contains(b, "..") || strings.Contains(b, "@{"):
		return errors.New(`a branch name cannot contain ".." or "@{"`)
	case strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.Contains(b, "//"):
		return errors.New("a branch name cannot start or end with /, or have an empty part")
	case strings.HasSuffix(b, "."):
		return errors.New(`a branch name cannot end with "."`)
	}
	for _, part := range strings.Split(b, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return errors.New(`no part of a branch name can start with "." or end with ".lock"`)
		}
	}
	return nil
}

func normaliseDeployOnly(rules []DeployOnlyRule) ([]DeployOnlyRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	if len(rules) > MaxDeployOnly {
		return nil, fmt.Errorf("at most %d rules", MaxDeployOnly)
	}
	out := make([]DeployOnlyRule, 0, len(rules))
	for i, rule := range rules {
		p := strings.TrimSpace(rule.Path)
		if err := validDeployPattern(p); err != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, err)
		}
		lines := rule.Lines
		if len(lines) > MaxDeployOnlyText || hasControl(lines) {
			return nil, fmt.Errorf("rule %d (%s): lines must be one line of at most %d bytes", i+1, p, MaxDeployOnlyText)
		}
		if _, err := (&scope.DeployOnlyRule{Lines: lines}).LinesRegexp(); err != nil {
			return nil, fmt.Errorf("rule %d (%s): lines is not a regular expression: %w", i+1, p, err)
		}
		out = append(out, DeployOnlyRule{Path: p, Lines: lines})
	}
	return out, nil
}

func normaliseSharedPaths(rules []SharedPathRule) ([]SharedPathRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	if len(rules) > MaxSharedPaths {
		return nil, fmt.Errorf("at most %d rules", MaxSharedPaths)
	}
	out := make([]SharedPathRule, 0, len(rules))
	for i, rule := range rules {
		p, err := normaliseRulePath(rule.Path)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, err)
		}
		switch rule.Resolve {
		case scope.ResolveMigration, scope.ResolveRegenerate, scope.ResolveMerge:
		default:
			return nil, fmt.Errorf("rule %d (%s): resolve must be %q, %q or %q, not %q", i+1, p,
				scope.ResolveMigration, scope.ResolveRegenerate, scope.ResolveMerge, rule.Resolve)
		}
		out = append(out, SharedPathRule{Path: p, Resolve: rule.Resolve})
	}
	return out, nil
}

func normaliseProtectedPaths(rules []ProtectedPathRule) ([]ProtectedPathRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	if len(rules) > MaxProtectedPaths {
		return nil, fmt.Errorf("at most %d rules", MaxProtectedPaths)
	}
	out := make([]ProtectedPathRule, 0, len(rules))
	seen := map[string]bool{}
	for i, rule := range rules {
		p, err := normaliseRulePath(rule.Path)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, err)
		}
		reason := strings.TrimSpace(rule.Reason)
		if len(reason) > MaxProtectedReason || hasControl(reason) {
			return nil, fmt.Errorf("rule %d (%s): reason must be one line of at most %d bytes", i+1, p, MaxProtectedReason)
		}
		if seen[p] {
			return nil, fmt.Errorf("rule %d: %s is listed twice", i+1, p)
		}
		seen[p] = true
		out = append(out, ProtectedPathRule{Path: p, Reason: reason})
	}
	return out, nil
}

// normaliseRulePath validates a path rule as shared_paths and protected_paths
// read it: a directory prefix when it ends in "/", otherwise a file pattern.
func normaliseRulePath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if len(p) > MaxDeployOnlyText {
		return "", fmt.Errorf("path is longer than %d bytes", MaxDeployOnlyText)
	}
	// A directory is the same check as a pattern once its slash is off.
	dir := strings.HasSuffix(p, "/")
	if err := validDeployPattern(strings.TrimSuffix(p, "/")); err != nil {
		return "", err
	}
	if dir && strings.ContainsAny(p, "*?[\\") {
		return "", fmt.Errorf("%s: a directory prefix cannot hold pattern characters", p)
	}
	return p, nil
}

// normaliseIndependentApps returns each entry as a directory prefix with its
// trailing slash. The slash is added when missing, since an app is always a
// directory; the repo's root ("/" or "") is refused because "everything" is
// not an app list.
func normaliseIndependentApps(apps []string) ([]string, error) {
	if len(apps) == 0 {
		return nil, nil
	}
	if len(apps) > MaxIndependentApps {
		return nil, fmt.Errorf("at most %d directories", MaxIndependentApps)
	}
	out := make([]string, 0, len(apps))
	seen := map[string]bool{}
	for i, a := range apps {
		dir := strings.TrimSuffix(strings.TrimSpace(a), "/")
		if err := validDeployPattern(dir); err != nil {
			return nil, fmt.Errorf("entry %d (%q): %w", i+1, a, err)
		}
		if strings.ContainsAny(dir, "*?[\\") {
			return nil, fmt.Errorf("entry %d (%q): a directory cannot hold pattern characters", i+1, a)
		}
		dir += "/"
		if seen[dir] {
			return nil, fmt.Errorf("entry %d: %s is listed twice", i+1, dir)
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out, nil
}

// validDeployPattern: a pattern the code host's paths can match, which are
// relative to the repo's root with no "." or ".." segments.
func validDeployPattern(p string) error {
	switch {
	case p == "":
		return errors.New("a path pattern is needed")
	case len(p) > MaxDeployOnlyText:
		return fmt.Errorf("path is longer than %d bytes", MaxDeployOnlyText)
	case hasControl(p):
		return errors.New("path has characters a path cannot")
	case strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/"):
		return errors.New("path is a file pattern relative to the repo's root, e.g. deploy/*/kustomization.yaml")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("path cannot have empty, \".\" or \"..\" segments")
		}
	}
	if _, err := path.Match(p, ""); err != nil {
		return fmt.Errorf("path %q is not a pattern: %w", p, err)
	}
	return nil
}

// validPath is the one place a path's shape is decided: a single directory
// name. A nested path would name somewhere the layout report never looks, so
// it could never match. The workspace root is not a repo, so "." is refused.
func validPath(p string) error {
	switch {
	case p == "":
		return errors.New("a path is needed: the repo's directory name")
	case p == ".":
		return errors.New("a repo cannot live at the workspace root (path \".\"): repos go in a subfolder, because the root holds slop-haus's managed AGENTS.md and CLAUDE.md; use the repo's name as its path")
	case p == "..":
		return errors.New("path cannot climb out of the workspace root")
	case strings.ContainsAny(p, `/\`):
		return errors.New("path must be a single directory name directly under the root")
	case len([]rune(p)) > agent.MaxLayoutPath:
		return fmt.Errorf("path is longer than %d characters", agent.MaxLayoutPath)
	case hasControl(p):
		return errors.New("path has characters a path cannot")
	case !cloneableName.MatchString(p) || strings.EqualFold(p, ".git"):
		return errors.New("path may use only letters, digits, \".\", \"_\" and \"-\", must not start with \".\" or \"-\", and cannot be .git")
	}
	return nil
}

// The shapes setup and update.sh will clone: the same two patterns
// are in assets/sh/sync-repos.sh, which does not trust what it is handed and
// would otherwise skip, with a message, a repo the manifest let through. A
// manifest that cannot be cloned from is refused here, where an owner sees it.
var (
	cloneableName   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	cloneableRemote = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*(/[A-Za-z0-9._-]+)+$`)
)

// hasDotSegment: "." and ".." (and a bare ".git") name no repository, and
// sync-repos.sh refuses them.
func hasDotSegment(remote string) bool {
	for _, seg := range strings.Split(remote, "/") {
		if seg == "." || seg == ".." || seg == ".git" {
			return true
		}
	}
	return false
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) >= 0
}

// RepoFor is the manifest repo a ticket scope belongs to: the one with the
// path contains it, or nil when the scope is in none of them.
func RepoFor(repos []Repo, scope string) *Repo {
	scope = strings.TrimPrefix(scope, "./")
	for i := range repos {
		r := &repos[i]
		if scope == r.Path || strings.HasPrefix(scope, r.Path+"/") {
			return r
		}
	}
	return nil
}
