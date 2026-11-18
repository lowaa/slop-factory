package manifest

import (
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// Protected is the rule of a repo's protected_paths that covers p, a path
// relative to the workspace root (as a ticket's scope is). Rules match like
// shared paths, in the repo RepoFor places p in.
func Protected(repos []Repo, p string) (*ProtectedPathRule, bool) {
	repo := RepoFor(repos, p)
	if repo == nil {
		return nil, false
	}
	rel := strings.TrimPrefix(p, repo.Path+"/")
	for i := range repo.ProtectedPaths {
		if scope.PathRuleMatches(repo.ProtectedPaths[i].Path, rel) {
			return &repo.ProtectedPaths[i], true
		}
	}
	return nil, false
}
