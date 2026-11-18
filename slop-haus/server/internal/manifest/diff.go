package manifest

import "slices"

// Ops of a RepoChange.
const (
	OpAdded   = "added"
	OpRemoved = "removed"
	OpChanged = "changed"
)

// RepoChange is what moving from one manifest to another does to one repo.
// The guards it loosens are what an owner approving it must see, so each rule
// is listed, not just counted.
type RepoChange struct {
	Path string `json:"path"`
	Op   string `json:"op" enum:"added,removed,changed"`
	// OldRemote is set when the remote changed (or the repo was removed), NewRemote
	// when it changed (or the repo was added).
	OldRemote string `json:"old_remote,omitempty"`
	NewRemote string `json:"new_remote,omitempty"`
	// BaseBranch is set only when it changed; an empty side is the repo's default branch.
	BaseBranch             *BranchChange       `json:"base_branch_changed,omitempty"`
	DeployOnlyAdded        []DeployOnlyRule    `json:"deploy_only_added,omitempty"`
	DeployOnlyRemoved      []DeployOnlyRule    `json:"deploy_only_removed,omitempty"`
	SharedPathsAdded       []SharedPathRule    `json:"shared_paths_added,omitempty"`
	SharedPathsRemoved     []SharedPathRule    `json:"shared_paths_removed,omitempty"`
	SharedPathsResolved    []SharedPathResolve `json:"shared_paths_resolve_changed,omitempty"`
	ProtectedPathsAdded    []ProtectedPathRule `json:"protected_paths_added,omitempty"`
	ProtectedPathsRemoved  []ProtectedPathRule `json:"protected_paths_removed,omitempty"`
	IndependentAppsAdded   []string            `json:"independent_apps_added,omitempty"`
	IndependentAppsRemoved []string            `json:"independent_apps_removed,omitempty"`
	// BatchWithinApps and SkipRetestAcrossApps are set only when the flag changed.
	BatchWithinApps      *FlagChange `json:"batch_within_apps_changed,omitempty"`
	SkipRetestAcrossApps *FlagChange `json:"skip_retest_across_apps_changed,omitempty"`
}

// FlagChange is a boolean setting that moved.
type FlagChange struct {
	Old bool `json:"old"`
	New bool `json:"new"`
}

// BranchChange is a base branch that moved.
type BranchChange struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// SharedPathResolve is a shared path whose resolution changed.
type SharedPathResolve struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// Diff is what turning old into next changes, by repo path, in path order.
// Both are taken as normalised. A nil old is a tenant with no manifest.
func Diff(old, next []Repo) []RepoChange {
	byPath := func(repos []Repo) map[string]*Repo {
		m := make(map[string]*Repo, len(repos))
		for i := range repos {
			m[repos[i].Path] = &repos[i]
		}
		return m
	}
	was, now := byPath(old), byPath(next)
	var paths []string
	for p := range was {
		paths = append(paths, p)
	}
	for p := range now {
		if was[p] == nil {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)

	var out []RepoChange
	for _, p := range paths {
		before, after := was[p], now[p]
		switch {
		case before == nil:
			c := diffRepo(&Repo{}, after)
			c.Op, c.Path, c.OldRemote = OpAdded, p, ""
			out = append(out, c)
		case after == nil:
			c := diffRepo(before, &Repo{})
			c.Op, c.Path, c.NewRemote = OpRemoved, p, ""
			out = append(out, c)
		default:
			if c := diffRepo(before, after); c.changed() {
				c.Op, c.Path = OpChanged, p
				out = append(out, c)
			}
		}
	}
	return out
}

func (c *RepoChange) changed() bool {
	return c.OldRemote != "" || c.BaseBranch != nil || len(c.DeployOnlyAdded)+len(c.DeployOnlyRemoved)+
		len(c.SharedPathsAdded)+len(c.SharedPathsRemoved)+len(c.SharedPathsResolved)+
		len(c.ProtectedPathsAdded)+len(c.ProtectedPathsRemoved)+
		len(c.IndependentAppsAdded)+len(c.IndependentAppsRemoved) > 0 || c.BatchWithinApps != nil || c.SkipRetestAcrossApps != nil
}

func diffRepo(before, after *Repo) RepoChange {
	var c RepoChange
	if before.Remote != after.Remote {
		c.OldRemote, c.NewRemote = before.Remote, after.Remote
	}
	if before.BaseBranch != after.BaseBranch {
		c.BaseBranch = &BranchChange{Old: before.BaseBranch, New: after.BaseBranch}
	}
	c.DeployOnlyAdded, c.DeployOnlyRemoved = setDiff(before.DeployOnly, after.DeployOnly,
		func(r DeployOnlyRule) string { return r.Path + "\x00" + r.Lines })
	hasShared := func(rules []SharedPathRule, path string) int {
		return slices.IndexFunc(rules, func(o SharedPathRule) bool { return o.Path == path })
	}
	for _, r := range after.SharedPaths {
		switch i := hasShared(before.SharedPaths, r.Path); {
		case i < 0:
			c.SharedPathsAdded = append(c.SharedPathsAdded, r)
		case before.SharedPaths[i].Resolve != r.Resolve:
			c.SharedPathsResolved = append(c.SharedPathsResolved, SharedPathResolve{Path: r.Path, Old: before.SharedPaths[i].Resolve, New: r.Resolve})
		}
	}
	for _, r := range before.SharedPaths {
		if hasShared(after.SharedPaths, r.Path) < 0 {
			c.SharedPathsRemoved = append(c.SharedPathsRemoved, r)
		}
	}
	// A changed reason is a change: it is the owner's own record of why.
	c.ProtectedPathsAdded, c.ProtectedPathsRemoved = setDiff(before.ProtectedPaths, after.ProtectedPaths,
		func(r ProtectedPathRule) string { return r.Path + "\x00" + r.Reason })
	c.IndependentAppsAdded, c.IndependentAppsRemoved = setDiff(before.IndependentApps, after.IndependentApps, func(s string) string { return s })
	if before.BatchWithinApps != after.BatchWithinApps {
		c.BatchWithinApps = &FlagChange{Old: before.BatchWithinApps, New: after.BatchWithinApps}
	}
	if before.SkipRetestAcrossApps != after.SkipRetestAcrossApps {
		c.SkipRetestAcrossApps = &FlagChange{Old: before.SkipRetestAcrossApps, New: after.SkipRetestAcrossApps}
	}
	return c
}

// setDiff is what next has that old lacks, and what old has that next lacks.
func setDiff[T any](old, next []T, key func(T) string) (added, removed []T) {
	has := func(set []T, k string) bool {
		return slices.ContainsFunc(set, func(v T) bool { return key(v) == k })
	}
	for _, v := range next {
		if !has(old, key(v)) {
			added = append(added, v)
		}
	}
	for _, v := range old {
		if !has(next, key(v)) {
			removed = append(removed, v)
		}
	}
	return added, removed
}
