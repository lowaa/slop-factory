package ticket

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Placement is where a set of scopes falls in the tenant's workspace
// manifest.
type Placement struct {
	// Declared is false for a tenant with no manifest: nothing below it
	// means anything, and nothing is enforced.
	Declared bool
	// All is every manifest repo.
	All []scope.WorkspaceRepo
	// Repos are the distinct manifest repos the scopes fall under.
	Repos []scope.WorkspaceRepo
	// Unplaced are the scopes that fall under none of them.
	Unplaced []string
}

// Workspace is what the ticket rules need to know about the tenant's
// workspace manifest. Declared in this package because the manifest package
// sits above it (it reads agents, which read tickets).
type Workspace interface {
	Place(ctx context.Context, scopes []string) (*Placement, error)
}

// WithWorkspace makes the service hold tickets to the workspace's manifest:
// one repo per ticket, and a PR that belongs to that repo. Without it, or
// for a tenant with no manifest, neither is enforced.
func WithWorkspace(w Workspace) ServiceOption {
	return func(s *Service) { s.workspace = w }
}

var (
	// ErrScopesSpanRepos is a scope set that is not inside exactly one
	// manifest repo. A change across repos needs a PR in each and cannot
	// land atomically, and a ticket has one pr_url.
	ErrScopesSpanRepos = errors.New("ticket: scopes must all be inside one repo of the workspace")
	// ErrPRWrongRepo is a pr_url that is not a PR of the ticket's repo.
	ErrPRWrongRepo = errors.New("ticket: the PR is not in the ticket's repo")
)

func scopePaths(paths []model.Path) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = p.Path
	}
	return out
}

// checkOneRepo refuses a scope set that spans manifest repos, or reaches
// outside all of them. A tenant with no manifest is never refused.
//
// Not retroactive: it runs when scopes are filed or changed, never over
// tickets already open, so adopting a manifest strands nothing.
func (s *Service) checkOneRepo(ctx context.Context, scopes []string) error {
	if s.workspace == nil || len(scopes) == 0 {
		return nil
	}
	pl, err := s.workspace.Place(ctx, scopes)
	if err != nil {
		return err
	}
	if !pl.Declared {
		return nil
	}
	if len(pl.Unplaced) > 0 {
		return fmt.Errorf("%w: %s not in any of the workspace's repos (%s)",
			ErrScopesSpanRepos, quoteList(pl.Unplaced), describeRepos(pl.All))
	}
	if len(pl.Repos) > 1 {
		return fmt.Errorf("%w: these span %s. A ticket changes one repo: split the work into one ticket per repo, "+
			"and order them with ticket_blocker_add", ErrScopesSpanRepos, describeRepos(pl.Repos))
	}
	return nil
}

func quoteList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	verb := q[0] + " is"
	if len(q) > 1 {
		verb = strings.Join(q, ", ") + " are"
	}
	return verb
}

func describeRepos(repos []scope.WorkspaceRepo) string {
	d := make([]string, len(repos))
	for i, r := range repos {
		d[i] = fmt.Sprintf("%s (%s)", r.Path, r.Remote)
	}
	return strings.Join(d, ", ")
}

// checkPRRepo refuses a PR link that is not in the repo the ticket's scopes
// are in. It says nothing when it cannot know that repo: no manifest, or
// scopes that fall under none or several repos -- a ticket filed before the
// manifest was adopted, which is not re-validated.
//
// A link on a host whose PRs this server cannot parse (one with no registered
// codehost.Host) is let through when it is under the repo's own path on the
// repo's own host: refusing it would stop
// every ticket in a repo on another forge from finishing, and the link has
// been checked as far as it can be (it is not checked to be a PR). Any other
// link, including one on a registered host that is not a PR (an issue, say), is
// refused: it is not a PR of that repo.
func (s *Service) checkPRRepo(ctx context.Context, scopes []string, prURL *string) error {
	if s.workspace == nil || prURL == nil || len(scopes) == 0 {
		return nil
	}
	pl, err := s.workspace.Place(ctx, scopes)
	if err != nil {
		return err
	}
	if !pl.Declared || len(pl.Unplaced) > 0 || len(pl.Repos) != 1 {
		return nil
	}
	repo := pl.Repos[0]
	if ref, ok := s.hosts.ParseChangeURL(*prURL); ok {
		if host, want, ok := s.hosts.ParseRemote(repo.Remote); !ok || ref.Host != host || ref.InRepo() != want {
			return fmt.Errorf("%w: the PR is in %s/%s/%s, but this ticket's repo is %s (%s)", ErrPRWrongRepo, ref.Host, ref.Owner, ref.Repo, repo.Path, repo.Remote)
		}
		return nil
	}
	u, err := url.Parse(*prURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPRURL, err)
	}
	remoteHost, remotePath, _ := strings.Cut(repo.Remote, "/")
	// Every forge's PR link starts with the repo's own path
	// (/group/project/-/merge_requests/7, /owner/repo/pulls/7), so that is
	// what is compared when the link's shape is not otherwise understood.
	if _, known := s.hosts.Host(remoteHost); !known && strings.EqualFold(u.Hostname(), remoteHost) &&
		strings.HasPrefix(strings.ToLower(u.Path), "/"+remotePath+"/") {
		return nil
	}
	return fmt.Errorf("%w: %q is not a pull request in this ticket's repo %s (%s)", ErrPRWrongRepo, *prURL, repo.Path, repo.Remote)
}

// inTicketsRepo is exit, once prURL is known to be a PR of the repo the held
// ticket's scopes are in. It is checked on the ticket as the transaction
// loaded it, so scopes edited since the agent last looked are the ones used.
// Review is held to it as well as done, so a wrong-repo PR is stopped before
// a human is asked to review it.
func (s *Service) inTicketsRepo(ctx context.Context, prURL *string, exit heldExit) heldExit {
	return func(held *Ticket, by uuid.UUID) (*Change, error) {
		if err := s.checkPRRepo(ctx, scopePaths(held.Paths), prURL); err != nil {
			return nil, err
		}
		return exit(held, by)
	}
}
