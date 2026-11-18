package scope

import (
	"path"
	"strings"
)

// The kinds of shared path, which tell a worker how a clash there is
// resolved. A closed set: the manifest refuses anything else.
const (
	// ResolveMigration is a directory of numbered migrations: renumber to
	// follow what landed, update LATEST, and check the migration still makes
	// sense after theirs.
	ResolveMigration = "migration"
	// ResolveRegenerate is generated output: re-run the generator after
	// merging main, never hand-merge it.
	ResolveRegenerate = "regenerate"
	// ResolveMerge is a file many tickets touch in small, usually
	// independent ways (docs, "everyone adds a line" files): the later
	// agent merges main and settles the clash by hand.
	ResolveMerge = "merge"
)

// SharedPathRule is one shared path of a manifest repo, as an owner declared
// it. Overlaps wholly inside it never keep a ticket from being claimed or
// extended: the clash is mechanical and Resolve says how it is settled.
type SharedPathRule struct {
	// Path is relative to the repo's own root: a directory prefix when it
	// ends in "/", otherwise a path.Match pattern that matches files.
	Path    string
	Resolve string
}

// SharedPaths answers whether the overlap of two scopes lies wholly inside a
// shared path. The zero value, and nil, have none.
type SharedPaths struct {
	repos []WorkspaceRepo
}

func NewSharedPaths(repos []WorkspaceRepo) *SharedPaths { return &SharedPaths{repos: repos} }

// Covers reports the resolve kind of the rule the overlap lies wholly
// inside. Only that repo's rules apply: a pattern of one repo says nothing
// about another's files.
func (s *SharedPaths) Covers(overlap string) (string, bool) {
	if s == nil {
		return "", false
	}
	repo := s.repoOf(overlap)
	if repo == nil {
		return "", false
	}
	rel := strings.TrimPrefix(overlap, repo.Path+"/")
	for _, rule := range repo.Shared {
		if PathRuleMatches(rule.Path, rel) {
			return rule.Resolve, true
		}
	}
	return "", false
}

// PathRuleMatches is the manifest's one path-rule matcher, shared by every
// rule list that takes a path (shared paths, protected paths): a rule ending
// in "/" covers everything under that directory, otherwise it is a path.Match
// pattern that matches files. rel is relative to the repo's root.
func PathRuleMatches(rule, rel string) bool {
	if strings.HasSuffix(rule, "/") {
		return strings.HasPrefix(rel, rule)
	}
	// A pattern names files: a directory is not wholly inside one.
	if strings.HasSuffix(rel, "/") {
		return false
	}
	ok, err := path.Match(rule, rel)
	return err == nil && ok
}

// repoOf is the repo with the longest path containing p, as the manifest
// places a scope.
func (s *SharedPaths) repoOf(p string) *WorkspaceRepo {
	var best *WorkspaceRepo
	for i := range s.repos {
		r := &s.repos[i]
		if !strings.HasPrefix(p, r.Path+"/") {
			continue
		}
		if best == nil || len(r.Path) > len(best.Path) {
			best = r
		}
	}
	return best
}

// OverlapOf is the part of the filesystem two conflicting scopes share: the
// narrower of them, since a directory contains whatever it holds.
func OverlapOf(a, b string) string {
	if a != b && strings.HasSuffix(a, "/") && strings.HasPrefix(b, a) {
		return b
	}
	return a
}

// SharedClash classifies two scopes: whether they conflict, and if so, the
// resolve kind of the shared path the whole overlap lies inside ("" when it
// is a real, exclusive conflict).
func SharedClash(a, b string, shared *SharedPaths) (conflict bool, resolve string) {
	if !Conflicts(a, b) {
		return false, ""
	}
	resolve, _ = shared.Covers(OverlapOf(a, b))
	return true, resolve
}

// resolveAdvice is what a worker does when merging the base branch conflicts in a path
// of that kind. ResolveAdvice is the one way out, so what a claim's notice
// and workspace_get tell a worker never drift apart.
var resolveAdvice = map[string]string{
	ResolveMigration:  "renumber your migration to follow what landed, update LATEST, and check it still makes sense after theirs",
	ResolveRegenerate: "re-run the generator after merging the base branch; never hand-merge it",
	ResolveMerge:      "merge the base branch and resolve the conflict by hand, keeping both sides' intent. Re-run the tests that cover the file. If both sides changed the same behaviour, not just neighbouring lines, ask with ticket_discuss rather than guess",
}

// ResolveAdvice is what to do when merging the base branch conflicts in a shared path of
// the given resolve kind, or "" for a kind that is not one.
func ResolveAdvice(kind string) string { return resolveAdvice[kind] }
