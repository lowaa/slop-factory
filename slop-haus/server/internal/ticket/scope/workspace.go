package scope

import "regexp"

// WorkspaceRepo is one repo of the tenant's workspace manifest, with its
// remote normalised (host/owner/repo, lower-case).
type WorkspaceRepo struct {
	Path   string
	Remote string
	// BaseBranch is the branch this tenant's changes to the repo merge into;
	// empty is the repo's default branch. Read only by BaseResolver.
	BaseBranch string
	// DeployOnly are the files of this repo only deploys change, which a
	// change's head may lag the base on and still merge.
	DeployOnly []DeployOnlyRule
	// Shared are the paths of this repo whose clashes never keep tickets
	// apart.
	Shared []SharedPathRule
	// IndependentApps are directory prefixes (trailing /) of apps that build
	// and deploy independently. BatchWithinApps gates merge batching on
	// them, SkipRetestAcrossApps the drift shortcut.
	IndependentApps      []string
	BatchWithinApps      bool
	SkipRetestAcrossApps bool
}

// DeployOnlyRule is one deploy-only file pattern of a manifest repo, as an
// owner declared it and the manifest validated it.
type DeployOnlyRule struct {
	// Path is a path.Match pattern, relative to the repo's own root.
	Path string
	// Lines, if set, is a regular expression every line a deploy adds to or
	// removes from a matching file must match in full.
	Lines string
}

// LinesRegexp is Lines as it is applied: to the whole line, never a part of
// it, so "newTag: .*" cannot be satisfied by a line that merely contains it.
// Nil when Lines is empty.
func (r *DeployOnlyRule) LinesRegexp() (*regexp.Regexp, error) {
	if r.Lines == "" {
		return nil, nil
	}
	return regexp.Compile(`^(?:` + r.Lines + `)$`)
}
