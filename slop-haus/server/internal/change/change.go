// Package change is slop-haus acting on a ticket's change (pull request) on
// the code host for the agent that holds the ticket: opening it, reading its
// checks and merging it. It is the merge gate. An agent no longer drives
// the host itself, so the rules the briefing only asks for are enforced
// here: the integration lock is held, the checks passed on the head that is
// merged, and a human approved where the ticket requires it.
//
// Everything reaches the host through codehost.Changes, which holds the
// credential; nothing here sees a token. The tenant is always the
// authenticated agent's, passed in by the caller from its identity and
// checked against the request's context, never read from a request.
package change

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// Tickets is the slice of ticket.Service the change flow uses.
type Tickets interface {
	Get(ctx context.Context, id int64) (*ticket.Ticket, error)
	Blockers(ctx context.Context, ticketID int64) (*ticket.Graph, error)
	Attempts(ctx context.Context, ticketID int64) ([]ticket.Attempt, error)
	ChangeGateFor(ctx context.Context, agentID uuid.UUID) (*ticket.ChangeGate, error)
	RecordChange(ctx context.Context, agentID uuid.UUID, prURL string, newChange bool, within func(q db.Querier) error) error
	Review(ctx context.Context, agentID uuid.UUID, params *ticket.ReviewParams) (*ticket.Ticket, error)
	Done(ctx context.Context, agentID uuid.UUID, params *ticket.DoneParams) (*ticket.Ticket, error)
	AddComment(ctx context.Context, ticketID int64, author ticket.Author, body string) (*ticket.Comment, error)
	NarrowToChange(ctx context.Context, agentID uuid.UUID, ticketID int64, changed []string, shared *scope.SharedPaths) (*ticket.NarrowResult, error)
}

// Names resolves who to name in a change's text.
type Names interface {
	Agent(ctx context.Context, id uuid.UUID) (string, error)
	User(ctx context.Context, id uuid.UUID) (string, error)
}

// Proposals is where the proposals a person approved for a ticket are read.
type Proposals interface {
	List(ctx context.Context, filter *pendingaction.ListFilter) ([]pendingaction.Action, error)
}

// Options are what a Service is built from.
type Options struct {
	Hosts     *codehost.Registry
	Tickets   Tickets
	Workspace ticket.Workspace
	Store     Store
	Names     Names
	// Proposals is optional: without it the change's text names no
	// proposal approvers.
	Proposals Proposals
	// Events records every operation on the host, and every refusal.
	Events *audit.Recorder
	// BoardURL is where the board is served, for the ticket link in a
	// change's text. Empty gives no link.
	BoardURL string
	// BaseBranch is the last fallback for the branch changes merge into,
	// used only when neither the tenant's manifest nor the code host names
	// one (ticket.BaseResolver). It is the server's setting, never the
	// agent's: letting an agent choose would let it merge into any branch of
	// the repository. Default "main".
	BaseBranch string
	// Detect decides when a head's changes are the reviewed ones. Required:
	// the merge gate does not guess what "the same change" means.
	Detect codehost.ChangeDetectionStrategy
	Logger slog.Handler
	// Trees is optional: without it a merge on batch evidence is refused.
	Trees TreeReader
}

// Service is the change flow.
type Service struct {
	hosts     *codehost.Registry
	tickets   Tickets
	workspace ticket.Workspace
	store     Store
	names     Names
	proposals Proposals
	events    *audit.Recorder
	boardURL  string
	bases     *ticket.BaseResolver
	detect    codehost.ChangeDetectionStrategy
	trees     TreeReader
	logger    *slog.Logger
}

func NewService(opts *Options) (*Service, error) {
	if opts.Hosts == nil || opts.Tickets == nil || opts.Workspace == nil || opts.Store == nil || opts.Names == nil || opts.Detect == nil || opts.Logger == nil {
		return nil, errors.New("change: Hosts, Tickets, Workspace, Store, Names, Detect and Logger are required")
	}
	s := &Service{
		hosts: opts.Hosts, tickets: opts.Tickets, workspace: opts.Workspace, store: opts.Store, names: opts.Names,
		proposals: opts.Proposals, events: opts.Events, boardURL: strings.TrimRight(opts.BoardURL, "/"),
		detect: opts.Detect, trees: opts.Trees, logger: slog.New(opts.Logger),
	}
	s.bases = ticket.NewBaseResolver(ticket.BaseOrDefault(opts.BaseBranch), s.logger)
	return s, nil
}

var (
	// ErrNoRepo is a ticket the workspace manifest does not place in exactly
	// one repository on a code host slop-haus can act on, so there is no
	// repository to open or merge a change in.
	ErrNoRepo = errors.New("change: the workspace manifest does not place this ticket in one repository on a supported code host")
	// ErrInvalid is a request the rules refuse before any host is called.
	ErrInvalid = errors.New("change: invalid request")
	// ErrLockNotHeld is a merge by an agent that does not hold the
	// integration lock.
	ErrLockNotHeld = errors.New("change: you do not hold the integration lock: take it with integration_lock first")
	// ErrNotApproved is a merge of a ticket that requires a human's approval
	// and has none.
	ErrNotApproved = errors.New("change: this ticket requires a human to approve its change, and none has: hand it over with ticket_finish")
	// ErrDraft is a merge of a change still in draft.
	ErrDraft = errors.New("change: the change is still a draft: mark it ready with change_open (draft false) first")
	// ErrAlreadyMerged is a merge, or a hand-over for review, of a change that
	// was merged already. The agent still holding the ticket cannot close it
	// (merging is what closes it), so the text sends it to a person.
	ErrAlreadyMerged = errors.New("change: PR already merged outside slop-haus; a person must close the ticket ('Close as merged'). Release with this reason.")
	// ErrChecksNotPassed is a merge whose head has no passing checks:
	// failed, still running, or none at all.
	ErrChecksNotPassed = errors.New("change: the checks have not passed on the change's head")
	// ErrHeadMismatch is a merge or a check read for a head other than the
	// one the change has on the host.
	ErrHeadMismatch = errors.New("change: the change's head is not the commit you named")
	// ErrHeadMoved is a merge that the host refused because the head moved
	// after its checks were read.
	ErrHeadMoved = fmt.Errorf("change: the change's head moved after its checks were read, so nothing was merged: "+
		"read the checks again with change_checks (%w)", codehost.ErrHeadMoved)
	// ErrHeadNotReviewed is a merge of a head the approval does not cover:
	// neither the commit a human was asked to review nor one whose changes
	// against the base are the same as the ones reviewed. Code pushed since,
	// or a base change that touched the reviewed lines, needs a new review.
	ErrHeadNotReviewed = errors.New("change: the change's head is neither the commit that was handed over for review nor one with the same changes against the base, so the approval does not cover it: " +
		"hand it over again with ticket_finish")
	// ErrBehindBase is a merge of a head that does not contain the base
	// branch's current tip: its checks ran against an older base.
	ErrBehindBase = errors.New("change: the change's head does not contain the base branch's latest commit")
	// ErrWrongRepo is a change whose repository is no longer the one the
	// ticket's scopes are in.
	ErrWrongRepo = errors.New("change: the change is not in the repository this ticket's scopes are in")
	// ErrWrongBase is a change that does not target the branch changes merge
	// into, as the tenant's manifest and the host name it now.
	ErrWrongBase = errors.New("change: the change does not target the base branch")
	// ErrNoHost is a change on a code host slop-haus is not set up to act on.
	ErrNoHost = errors.New("change: slop-haus cannot act on this code host")
)

// tenantOf checks that the tenant the caller names, from the agent's
// authenticated identity, is the one the request's context is scoped to.
// They are the same unless something is wired wrongly; refusing then is what
// keeps a mix-up from acting on one tenant's repository with another's
// credential.
func tenantOf(ctx context.Context, tenant uuid.UUID) error {
	scoped, err := db.TenantFrom(ctx)
	if err != nil {
		return err
	}
	if scoped != tenant {
		return errors.New("change: the tenant of the call does not match the tenant of its context")
	}
	return nil
}

func (s *Service) record(ctx context.Context, action audit.Action, attrs ...any) {
	if s.events != nil {
		s.events.Record(ctx, action, attrs...)
	}
}

// target is the repository a ticket's change lives in, and the host's
// operations on it.
type target struct {
	host    string
	repo    codehost.Repo
	changes codehost.Changes
	// deployOnly is the repo's deploy-only files, from the manifest.
	deployOnly []scope.DeployOnlyRule
	// repoPath is where the repo is checked out, relative to the workspace
	// root; shared, independentApps and skipRetestAcrossApps are its
	// manifest entry's, for the independent-apps rule.
	repoPath             string
	shared               []scope.SharedPathRule
	independentApps      []string
	skipRetestAcrossApps bool
	// base is the branch the ticket's change merges into, and lock the
	// integration lock merging into it takes (ticket.LockNameFor).
	base string
	lock string
}

// targetFor places the ticket's scopes in the workspace manifest. It fails
// closed: no manifest, scopes in several repositories or in none, or a
// remote on an unknown host all refuse.
func (s *Service) targetFor(ctx context.Context, t *ticket.Ticket) (*target, error) {
	tenantID, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	scopes := make([]string, len(t.Paths))
	for i, p := range t.Paths {
		scopes[i] = p.Path
	}
	pl, err := s.workspace.Place(ctx, scopes)
	if err != nil {
		return nil, err
	}
	if !pl.Declared || len(pl.Unplaced) > 0 || len(pl.Repos) != 1 {
		return nil, ErrNoRepo
	}
	host, repo, ok := s.hosts.ParseRemote(pl.Repos[0].Remote)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoRepo, pl.Repos[0].Remote)
	}
	changes, ok := s.hosts.Changes(host)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoHost, host)
	}
	r := &pl.Repos[0]
	rb, err := s.bases.Resolve(ctx, &ticket.BaseQuery{Tenant: tenantID, Repo: repo, Changes: changes, Manifest: r})
	if err != nil {
		return nil, fmt.Errorf("the base branch of %s could not be resolved: %w", r.Remote, err)
	}
	return &target{host: host, repo: repo, changes: changes, deployOnly: r.DeployOnly, repoPath: r.Path, shared: r.Shared,
		independentApps: r.IndependentApps, skipRetestAcrossApps: r.SkipRetestAcrossApps,
		base: rb.Branch, lock: ticket.LockNameFor(r.Path, rb)}, nil
}

// who is the people a change's text names. approvedBy is the approver of the
// agent's own attempt, not the ticket's promoted one.
func (s *Service) who(ctx context.Context, t *ticket.Ticket, approvedBy *uuid.UUID, agentID uuid.UUID) *People {
	p := &People{}
	p.Agent = s.agentName(ctx, agentID)
	switch {
	case t.CreatedByAgentID != nil:
		p.Creator = s.agentName(ctx, *t.CreatedByAgentID)
	case t.CreatedByUserID != nil:
		p.Creator = s.userName(ctx, *t.CreatedByUserID)
	}
	if approvedBy != nil {
		p.Approver = s.userName(ctx, *approvedBy)
	}
	p.ProposalApprovers = s.proposalApprovers(ctx, t.ID)
	return p
}

func (s *Service) agentName(ctx context.Context, id uuid.UUID) string {
	name, err := s.names.Agent(ctx, id)
	if err != nil {
		s.logger.Warn("change: resolve agent name", "agent_id", id, "err", err)
		return ""
	}
	return personName(name)
}

func (s *Service) userName(ctx context.Context, id uuid.UUID) string {
	name, err := s.names.User(ctx, id)
	if err != nil {
		s.logger.Warn("change: resolve user name", "user_id", id, "err", err)
		return ""
	}
	return personName(name)
}

func (s *Service) proposalApprovers(ctx context.Context, ticketID int64) []string {
	if s.proposals == nil {
		return nil
	}
	actions, err := s.proposals.List(ctx, &pendingaction.ListFilter{TicketID: &ticketID, States: []pendingaction.State{pendingaction.StateApproved}})
	if err != nil {
		s.logger.Warn("change: list approved proposals", "ticket_id", ticketID, "err", err)
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for i := range actions {
		a := &actions[i]
		if a.DecidedByUserID == nil {
			continue
		}
		entry := fmt.Sprintf("%s (%s)", s.userName(ctx, *a.DecidedByUserID), a.Kind)
		if !seen[entry] {
			seen[entry] = true
			names = append(names, entry)
		}
	}
	return names
}

func (s *Service) ticketURL(tenant uuid.UUID, id int64) string {
	if s.boardURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/tickets/%d?tenant=%s", s.boardURL, id, tenant)
}

func validTitle(title string) error {
	if strings.TrimSpace(title) == "" || len(title) > 256 {
		return fmt.Errorf("%w: the title must be 1 to 256 bytes", ErrInvalid)
	}
	for _, r := range title {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: the title must be one line", ErrInvalid)
		}
	}
	return nil
}

const maxBodyBytes = 30000

// validBranch is git's own rules for a ref name, in the part that matters
// here: a branch name that is neither a flag nor a way to name something
// other than a branch.
func validBranch(name string) error {
	switch {
	case name == "" || len(name) > 200:
		return fmt.Errorf("%w: the branch must be 1 to 200 bytes", ErrInvalid)
	case strings.HasPrefix(name, "-") || strings.HasPrefix(name, "refs/") || strings.Contains(name, "..") ||
		strings.Contains(name, "@{") || name == "@":
		return fmt.Errorf("%w: %q is not a branch name", ErrInvalid, name)
	}
	// Git's rules apply to each slash-separated component, which may not be
	// empty, start with a dot, or end with a dot or ".lock".
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("%w: %q is not a branch name", ErrInvalid, name)
		}
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune("~^:?*[\\", r) {
			return fmt.Errorf("%w: %q is not a branch name", ErrInvalid, name)
		}
	}
	return nil
}
