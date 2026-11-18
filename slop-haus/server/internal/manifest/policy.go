package manifest

import (
	"context"
	"slices"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// Policy answers the ticket service's questions about the manifest. It reads
// only the store, so the ticket service can hold it without a cycle through
// the agents the full Service reads.
type Policy struct {
	store Store
}

func NewPolicy(store Store) *Policy { return &Policy{store: store} }

var _ ticket.Workspace = (*Policy)(nil)

// Place says which manifest repos scopes fall under.
func (p *Policy) Place(ctx context.Context, scopes []string) (*ticket.Placement, error) {
	m, err := p.store.Get(ctx)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return &ticket.Placement{}, nil
	}
	pl := &ticket.Placement{Declared: true}
	for i := range m.Repos {
		pl.All = append(pl.All, workspaceRepo(&m.Repos[i]))
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		r := RepoFor(m.Repos, scope)
		switch {
		case r == nil:
			pl.Unplaced = append(pl.Unplaced, scope)
		case !seen[r.Path]:
			seen[r.Path] = true
			pl.Repos = append(pl.Repos, workspaceRepo(r))
		}
	}
	return pl, nil
}

func workspaceRepo(r *Repo) scope.WorkspaceRepo {
	out := scope.WorkspaceRepo{Path: r.Path, Remote: r.Remote, BaseBranch: r.BaseBranch}
	for _, rule := range r.DeployOnly {
		out.DeployOnly = append(out.DeployOnly, scope.DeployOnlyRule{Path: rule.Path, Lines: rule.Lines})
	}
	for _, rule := range r.SharedPaths {
		out.Shared = append(out.Shared, scope.SharedPathRule{Path: rule.Path, Resolve: rule.Resolve})
	}
	out.IndependentApps = slices.Clone(r.IndependentApps)
	out.BatchWithinApps = r.BatchWithinApps
	out.SkipRetestAcrossApps = r.SkipRetestAcrossApps
	return out
}
