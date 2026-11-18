package ticket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// BaseQuery is what a BaseSource may use to name the branch a ticket merges
// into: the repository the ticket's scopes are in, and its manifest entry.
type BaseQuery struct {
	Tenant  uuid.UUID
	Repo    codehost.Repo
	Changes codehost.Changes
	// Manifest is the ticket's repo as the workspace manifest declares it.
	Manifest *scope.WorkspaceRepo
}

// ResolvedBase is the branch a ticket merges into, and where the answer came
// from.
type ResolvedBase struct {
	Branch string
	// Explicit is whether an owner chose the branch (the manifest's
	// base_branch) rather than it being the repo's default. Only an explicit
	// base gets a lock of its own (LockNameFor).
	Explicit bool
	// Head is the branch's head commit when the source that named it read
	// that too, so the caller need not read it again. Nil otherwise.
	Head *codehost.BranchHead
}

// DefaultBaseBranch is the last-resort base branch: the one used when neither
// the tenant's manifest nor the code host names one. Every default goes
// through BaseOrDefault so it is applied in one place.
const DefaultBaseBranch = "main"

// BaseOrDefault is branch, or DefaultBaseBranch when it is empty.
func BaseOrDefault(branch string) string {
	if branch == "" {
		return DefaultBaseBranch
	}
	return branch
}

// BaseSource is one link of the base-branch chain. It returns nil when it has
// no opinion for the query and the next source is asked; an error stops the
// chain, so a host that cannot answer is never read as "use the next one".
type BaseSource func(ctx context.Context, q *BaseQuery) (*ResolvedBase, error)

// BaseResolver is the single answer to "which branch does this ticket merge
// into?". It asks its sources in order and takes the first answer.
//
// The chain today is the tenant's manifest base_branch for the repo, then the
// repo's default branch on the host, then a fixed fallback. A superticket with
// a base branch of its own is one more source in front of the manifest's: a
// "parent ticket's base" source, which would need the ticket in BaseQuery. No
// caller names a branch of its own, so adding it changes this chain only.
type BaseResolver struct {
	sources []BaseSource
}

// NewBaseResolver builds the standard chain. fallback is the branch used when
// neither the manifest nor the host names one.
func NewBaseResolver(fallback string, logger *slog.Logger) *BaseResolver {
	return &BaseResolver{sources: []BaseSource{manifestBase, hostDefaultBase(logger), fixedBase(fallback)}}
}

// Resolve names the branch the query's ticket merges into.
func (r *BaseResolver) Resolve(ctx context.Context, q *BaseQuery) (*ResolvedBase, error) {
	for _, src := range r.sources {
		rb, err := src(ctx, q)
		if err != nil {
			return nil, err
		}
		if rb != nil {
			return rb, nil
		}
	}
	return nil, errors.New("ticket: no source named the base branch")
}

func manifestBase(_ context.Context, q *BaseQuery) (*ResolvedBase, error) {
	if q.Manifest == nil || q.Manifest.BaseBranch == "" {
		return nil, nil
	}
	return &ResolvedBase{Branch: q.Manifest.BaseBranch, Explicit: true}, nil
}

// hostDefaultBase is the repo's default branch. A host that does not know the
// repository (or names no branch) has no opinion; any other failure stops the
// chain: guessing a branch when the host did not answer would merge into the
// wrong one.
func hostDefaultBase(logger *slog.Logger) BaseSource {
	return func(ctx context.Context, q *BaseQuery) (*ResolvedBase, error) {
		head, err := q.Changes.DefaultBranchHead(ctx, q.Tenant, q.Repo)
		switch {
		case errors.Is(err, codehost.ErrChangeNotFound):
			logger.Debug("ticket: host has no default branch for the repo", "repo", q.Repo.Owner+"/"+q.Repo.Name)
			return nil, nil
		case err != nil:
			return nil, fmt.Errorf("read the default branch of %s/%s: %w", q.Repo.Owner, q.Repo.Name, err)
		case head.Branch == "":
			return nil, nil
		}
		return &ResolvedBase{Branch: head.Branch, Head: head}, nil
	}
}

func fixedBase(branch string) BaseSource {
	return func(context.Context, *BaseQuery) (*ResolvedBase, error) {
		return &ResolvedBase{Branch: branch}, nil
	}
}

// LockNameFor is the integration lock a base branch is merged under. A repo
// on its default branch uses the tenant's one lock (IntegrationLockName), as
// before base branches existed, so locks held across a deploy still count;
// an explicit base branch gets a lock per (repo, branch), so tickets merging
// into different branches never queue behind each other.
func LockNameFor(repoPath string, rb *ResolvedBase) string {
	if rb == nil || !rb.Explicit {
		return IntegrationLockName
	}
	return BranchLockName(repoPath, rb.Branch)
}

// BranchLockName is the lock of one explicit base branch in a repo, for a
// caller that has the branch but no resolution to build it from.
func BranchLockName(repoPath, branch string) string {
	return fmt.Sprintf("%s:%s@%s", IntegrationLockName, repoPath, branch)
}

// ParseLockName is the repo and branch a lock name (see LockNameFor) serves;
// both are empty for the default lock.
func ParseLockName(name string) (repo, branch string) {
	rest, ok := strings.CutPrefix(name, IntegrationLockName+":")
	if !ok {
		return "", ""
	}
	// A git branch name may contain "@" and a repo path in practice does not,
	// so the first one splits.
	i := strings.Index(rest, "@")
	if i < 0 {
		return rest, ""
	}
	return rest[:i], rest[i+1:]
}
