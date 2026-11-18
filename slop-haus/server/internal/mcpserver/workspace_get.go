package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// Manifests is the slice of manifest.Service workspace_get reads. The
// tenant is the caller's, taken from the request's context: there is no way
// to name another.
type Manifests interface {
	Get(ctx context.Context) (*manifest.Manifest, error)
	// Revision is the manifest's revision, also while none is declared.
	Revision(ctx context.Context) (int64, error)
}

func registerWorkspaceGet(server *mcp.Server, h *Handler) {
	addTool(server, h, &mcp.Tool{
		Name: "workspace_get",
		Description: "Read your tenant's workspace manifest: each repo's path and remote, its deploy-only rules, and its shared paths with how " +
			"a conflict in each is resolved. Use it to check how a path is treated before claiming or extending scope, " +
			"or, in a console, to review the settings with your person. Read-only: an owner changes the manifest on the board. " +
			"A tenant that has declared none gets an empty list: nothing is then enforced.",
	}, leaseFree, h.workspaceGet)
}

type workspaceGetInput struct{}

type workspaceGetOutput struct {
	toolNotes
	Revision  int64           `json:"revision" jsonschema:"the manifest's revision: pass it as base_revision to propose_manifest_edit; it is kept when the manifest is cleared"`
	Declared  bool            `json:"declared" jsonschema:"false while the tenant has declared no manifest"`
	Repos     []workspaceRepo `json:"repos"`
	UpdatedAt *time.Time      `json:"updated_at,omitempty"`
	UpdatedBy *string         `json:"updated_by,omitempty" jsonschema:"the user who last saved it; absent if they have since been deleted"`
}

type workspaceRepo struct {
	Path                 string                `json:"path" jsonschema:"where the repo is checked out under the workspace root: one directory name"`
	Remote               string                `json:"remote"`
	BaseBranch           string                `json:"base_branch,omitempty" jsonschema:"the branch this tenant's changes to the repo merge into; absent means the repo's default branch. Nothing reads it yet"`
	DeployOnly           []workspaceDeployOnly `json:"deploy_only"`
	SharedPaths          []workspaceSharedPath `json:"shared_paths"`
	ProtectedPaths       []workspaceProtected  `json:"protected_paths" jsonschema:"paths that are always the owner's decision for the steward's judgment categories; owner-set. Keep them when you propose a manifest edit"`
	IndependentApps      []string              `json:"independent_apps" jsonschema:"directory prefixes (ending in /) of apps that build and deploy independently; empty when none is declared"`
	BatchWithinApps      bool                  `json:"batch_within_apps" jsonschema:"whether the landing queue may batch changes that each touch one of the independent_apps (also needs merge_queue_batch_size above 1); off by default"`
	SkipRetestAcrossApps bool                  `json:"skip_retest_across_apps" jsonschema:"whether a change behind main may merge on its existing CI when main moved only in other independent_apps; off by default"`
}

type workspaceDeployOnly struct {
	Path  string `json:"path" jsonschema:"file pattern relative to the repo's root"`
	Lines string `json:"lines,omitempty" jsonschema:"regular expression every changed line must match for a commit to count as a deploy; empty for any line"`
}

type workspaceProtected struct {
	Path   string `json:"path" jsonschema:"directory prefix when it ends in /, otherwise a file pattern, relative to the repo's root"`
	Reason string `json:"reason,omitempty"`
}

type workspaceSharedPath struct {
	Path    string `json:"path" jsonschema:"directory prefix when it ends in /, otherwise a file pattern, relative to the repo's root"`
	Resolve string `json:"resolve" jsonschema:"migration or regenerate"`
	OnClash string `json:"on_clash" jsonschema:"what to do when merging the base branch conflicts there"`
}

func (h *Handler) workspaceGet(ctx context.Context, _ uuid.UUID, _ workspaceGetInput) (*mcp.CallToolResult, workspaceGetOutput, error) {
	m, err := h.manifests.Get(ctx)
	if err != nil {
		return nil, workspaceGetOutput{}, err
	}
	if m == nil {
		rev, err := h.manifests.Revision(ctx)
		if err != nil {
			return nil, workspaceGetOutput{}, err
		}
		return text("This tenant has declared no workspace manifest: nothing is enforced. Revision %d.", rev), workspaceGetOutput{Revision: rev, Repos: []workspaceRepo{}}, nil
	}
	out := workspaceGetOutput{Declared: true, Revision: m.Revision, Repos: make([]workspaceRepo, 0, len(m.Repos)), UpdatedAt: &m.UpdatedAt}
	if m.UpdatedBy != nil {
		by := m.UpdatedBy.String()
		out.UpdatedBy = &by
	}
	var b strings.Builder
	for _, r := range m.Repos {
		repo := workspaceRepo{Path: r.Path, Remote: r.Remote, BaseBranch: r.BaseBranch, DeployOnly: []workspaceDeployOnly{}, SharedPaths: []workspaceSharedPath{}, ProtectedPaths: []workspaceProtected{},
			IndependentApps: []string{}, BatchWithinApps: r.BatchWithinApps, SkipRetestAcrossApps: r.SkipRetestAcrossApps}
		fmt.Fprintf(&b, "%s  %s\n", r.Path, r.Remote)
		if r.BaseBranch != "" {
			fmt.Fprintf(&b, "  base branch: %s\n", r.BaseBranch)
		}
		for _, d := range r.DeployOnly {
			repo.DeployOnly = append(repo.DeployOnly, workspaceDeployOnly{Path: d.Path, Lines: d.Lines})
			fmt.Fprintf(&b, "  deploy-only: %s", d.Path)
			if d.Lines != "" {
				fmt.Fprintf(&b, " (lines matching %s)", d.Lines)
			}
			b.WriteString("\n")
		}
		for _, s := range r.SharedPaths {
			advice := scope.ResolveAdvice(s.Resolve)
			repo.SharedPaths = append(repo.SharedPaths, workspaceSharedPath{Path: s.Path, Resolve: s.Resolve, OnClash: advice})
			fmt.Fprintf(&b, "  shared (%s): %s -- if merging the base branch conflicts there, %s\n", s.Resolve, s.Path, advice)
		}
		for _, p := range r.ProtectedPaths {
			repo.ProtectedPaths = append(repo.ProtectedPaths, workspaceProtected{Path: p.Path, Reason: p.Reason})
			fmt.Fprintf(&b, "  protected: %s", p.Path)
			if p.Reason != "" {
				fmt.Fprintf(&b, " (%s)", p.Reason)
			}
			b.WriteString("\n")
		}
		if len(r.IndependentApps) > 0 {
			repo.IndependentApps = r.IndependentApps
			fmt.Fprintf(&b, "  independent apps (batch within apps: %t, skip re-test across apps: %t): %s\n", r.BatchWithinApps, r.SkipRetestAcrossApps, strings.Join(r.IndependentApps, " "))
		}
		out.Repos = append(out.Repos, repo)
	}
	fmt.Fprintf(&b, "Revision %d, updated %s.", m.Revision, m.UpdatedAt.UTC().Format(time.RFC3339))
	return text("%s", b.String()), out, nil
}
