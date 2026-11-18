package change

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// OpenInput is a change to open for the held ticket.
type OpenInput struct {
	// Branch is the pushed branch the change is opened from.
	Branch string
	Title  string
	// Body is the agent's description; the server adds a footer naming the
	// ticket and the people involved.
	Body  string
	Draft bool
}

// Opened is the change the ticket now has.
type Opened struct {
	URL    string
	Number int64
	Branch string
	Draft  bool
	// Existing is a change already open from this branch, returned rather
	// than opened again.
	Existing bool
}

// Open opens a change from a pushed branch onto the base branch, in the
// repository the ticket's scopes are in, and records it on the ticket.
//
// Opening again from the same branch is how a draft becomes ready: with
// Draft false it marks the existing change ready. Opening from another
// branch supersedes the ticket's earlier change, which the server will no
// longer merge. Any new change drops the ticket's review outcome: an approval
// was of some other change, so a ticket that requires one is reviewed again.
func (s *Service) Open(ctx context.Context, tenant, agentID uuid.UUID, in *OpenInput) (*Opened, error) {
	if err := tenantOf(ctx, tenant); err != nil {
		return nil, err
	}
	if err := validateOpen(in); err != nil {
		return nil, err
	}
	gate, err := s.tickets.ChangeGateFor(ctx, agentID)
	if err != nil {
		return nil, err
	}
	t := gate.Ticket
	if t.NeedsDiscussion {
		return nil, fmt.Errorf("%w (ticket #%d)", ticket.ErrNeedsDiscussion, t.ID)
	}
	tgt, err := s.targetFor(ctx, t)
	if err != nil {
		return nil, err
	}
	if in.Branch == tgt.base {
		return nil, fmt.Errorf("%w: a change cannot be opened from the branch it merges into (%s)", ErrInvalid, tgt.base)
	}
	base, err := baseFor(gate, in, tgt)
	if err != nil {
		return nil, err
	}
	// Once the ticket has chosen, a change that is not the chosen one's says
	// so from its first moment on the host.
	if !gate.Promoted && gate.TicketPromoted {
		in = &OpenInput{Branch: in.Branch, Title: doNotMergeTitle(in.Title), Body: in.Body, Draft: in.Draft}
	}

	current, err := s.store.Current(ctx, gate.Attempt.TicketID, gate.Attempt.Number)
	if errors.Is(err, ErrNoChange) {
		current, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	if current != nil && current.Merging {
		return nil, ErrMergeInFlight
	}
	reopens := current != nil && current.MergedAt == nil && current.ClosedAt == nil && current.Head == in.Branch && current.Base == base && current.Change.InRepo() == tgt.repo && current.Change.Host == tgt.host
	if err := checkOwnBranch(gate, in.Branch, reopens); err != nil {
		return nil, err
	}
	if !gate.Promoted {
		if err := s.checkShadowBase(ctx, tenant, agentID, t.ID, t.BaseCommit, tgt, in.Branch, in.Branch); err != nil {
			return nil, err
		}
	}
	// Base is part of "the same change": a record made for another base (the
	// attempt has since been promoted) is not returned as this call's change.
	if reopens {
		return s.reopenSame(ctx, tenant, agentID, tgt, current, in)
	}

	// The claim already made this branch for an unpromoted attempt; it is made
	// again here for the cases the claim did not cover: a host that was down
	// then, or an attempt demoted after its claim. A network call, so outside
	// the transaction below and the claim lock. Idempotent, so it finds the
	// branch the claim or a cut-off open made.
	if !gate.Promoted {
		if err := tgt.changes.CreateBranch(ctx, tenant, tgt.repo, base, *t.BaseCommit); err != nil {
			s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, t.ID, "create_base_branch", "host", tgt.host, "repo", tgt.repo.Owner+"/"+tgt.repo.Name, "branch", base, "err", err.Error())...)
			return nil, fmt.Errorf("the code host could not create the base branch %s that this attempt's change targets (not a fault of your branch; you cannot fix it, raise it with ticket_discuss): %w", base, err)
		}
	}

	people := s.who(ctx, t, gate.Review.ApprovedByUserID, agentID)
	ref, err := tgt.changes.OpenChange(ctx, tenant, &codehost.OpenRequest{
		Repo: tgt.repo, Head: in.Branch, Base: base, Title: in.Title,
		Body: in.Body + "\n\n" + footer(t.ID, s.ticketURL(tenant, t.ID), people), Draft: in.Draft,
	})
	if err != nil {
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, t.ID, "open", "host", tgt.host, "repo", tgt.repo.Owner+"/"+tgt.repo.Name, "branch", in.Branch, "err", err.Error())...)
		return nil, err
	}
	url := s.hosts.ChangeURL(ref)
	// Read now, while the branch is where it was pushed: once the worker
	// merges main in, the merge base is no longer the branch point.
	baseCommit := ""
	if t.BaseCommit == nil {
		baseCommit = s.branchPoint(ctx, tenant, tgt, ref, t.ID)
	}
	// The change exists on the host from here; whatever fails below, the
	// record of it having been opened is already made.
	s.record(ctx, audit.ChangeOpened, s.attrs(tenant, agentID, t.ID, "open", "change", url, "draft", in.Draft, "branch", in.Branch)...)
	// One transaction: the ticket's link, the review outcome dropped and the
	// record a merge acts on. Whatever review the ticket has had was of some
	// other change -- an earlier one, merged or not, or one the agent
	// reported -- so it goes with the new one, and cannot outlive the record.
	var rec *Record
	err = s.tickets.RecordChange(ctx, agentID, url, true, func(q db.Querier) error {
		var err error
		rec, err = s.store.Open(ctx, q, &Record{TicketID: t.ID, Change: ref, Head: in.Branch, Base: base, Draft: in.Draft, OpenedBy: agentID})
		if err != nil || baseCommit == "" {
			return err
		}
		return s.store.SetBaseCommit(ctx, q, t.ID, baseCommit)
	})
	if err != nil {
		return nil, fmt.Errorf("the change was opened at %s but could not be recorded on the ticket: %w", url, err)
	}
	// The change is created on the host before it is recorded, so the ticket
	// can have been finished in between: the rule is checked once more now
	// that the record exists.
	s.TicketsSettled(ctx, []int64{t.ID})
	return &Opened{URL: url, Number: rec.Change.Number, Branch: rec.Head, Draft: rec.Draft}, nil
}

// branchPoint is the commit of the base branch the change starts from, or ""
// when the host cannot say: the ticket then has no base and is not
// replayable, but the change is still opened.
func (s *Service) branchPoint(ctx context.Context, tenant uuid.UUID, tgt *target, ref codehost.ChangeRef, ticketID int64) string {
	sha, err := s.mergeBase(ctx, tenant, tgt, ref)
	if err != nil {
		s.logger.Warn("change: base commit not recorded", "ticket", ticketID, "err", err)
		return ""
	}
	return sha
}

func (s *Service) mergeBase(ctx context.Context, tenant uuid.UUID, tgt *target, ref codehost.ChangeRef) (string, error) {
	changes := tgt.changes
	checks, err := changes.Checks(ctx, tenant, ref)
	if err != nil {
		return "", err
	}
	diff, err := changes.Diff(ctx, tenant, ref, checks.HeadSHA)
	if err != nil {
		return "", err
	}
	if diff.Base != tgt.base {
		return "", fmt.Errorf("the change targets %q, not %q", diff.Base, tgt.base)
	}
	return diff.MergeBaseSHA, nil
}

func (s *Service) reopenSame(ctx context.Context, tenant, agentID uuid.UUID, tgt *target, current *Record, in *OpenInput) (*Opened, error) {
	url := s.hosts.ChangeURL(current.Change)
	// Repairs the ticket's link should an earlier call have been cut off
	// between recording the change and writing it there.
	if err := s.tickets.RecordChange(ctx, agentID, url, false, nil); err != nil {
		return nil, fmt.Errorf("the change is open at %s but its link could not be put on the ticket: %w", url, err)
	}
	if current.Draft && !in.Draft {
		if err := tgt.changes.MarkReady(ctx, tenant, current.Change); err != nil {
			s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, current.TicketID, "mark_ready", "change", url, "err", err.Error())...)
			return nil, err
		}
		s.record(ctx, audit.ChangeMarkedReady, s.attrs(tenant, agentID, current.TicketID, "mark_ready", "change", url)...)
		if err := s.store.MarkReady(ctx, current.ID); err != nil {
			return nil, fmt.Errorf("the change at %s is ready but that could not be recorded: %w", url, err)
		}
		current.Draft = false
	}
	return &Opened{URL: url, Number: current.Change.Number, Branch: current.Head, Draft: current.Draft, Existing: true}, nil
}

// checkOwnBranch refuses a head branch that is not the held attempt's own
// (ticket.BranchName), so two attempts or tickets never push to one branch and
// an attempt cannot open a change from a sibling's. The one exception is the
// change the attempt already has recorded under another name, from before names
// were enforced: it may keep being re-opened, so tickets in flight at the
// deploy are not stranded.
func checkOwnBranch(gate *ticket.ChangeGate, branch string, reopensRecorded bool) error {
	want := ticket.BranchName(gate.Ticket.ID, gate.Attempt.Number)
	if branch == want || reopensRecorded {
		return nil
	}
	return fmt.Errorf("%w: this attempt's branch is %s, not %s; move your work onto it with `git push origin HEAD:%s`, then open the change from %s",
		ErrInvalid, want, branch, want, want)
}

// baseFor is the branch the held attempt's change merges into: the
// tenant's base branch for the repository for the promoted attempt, the ticket's own base
// branch for any other, whose change must never reach the repository's.
func baseFor(gate *ticket.ChangeGate, in *OpenInput, tgt *target) (string, error) {
	if gate.Promoted {
		return tgt.base, nil
	}
	if !in.Draft {
		return "", fmt.Errorf("change_open with draft false refused: %w", ticket.ErrAttemptAwaitsChoice)
	}
	if gate.Ticket.BaseCommit == nil {
		return "", fmt.Errorf("%w: the ticket has no pinned base commit for this attempt's base branch", ErrInvalid)
	}
	base := ticket.UnpromotedBase(gate.Ticket.ID)
	if in.Branch == base {
		return "", fmt.Errorf("%w: a change cannot be opened from the branch it merges into (%s)", ErrInvalid, base)
	}
	return base, nil
}

func validateOpen(in *OpenInput) error {
	if err := validBranch(in.Branch); err != nil {
		return err
	}
	if err := validTitle(in.Title); err != nil {
		return err
	}
	if len(in.Body) > maxBodyBytes {
		return fmt.Errorf("%w: the body is longer than %d bytes", ErrInvalid, maxBodyBytes)
	}
	return nil
}

// attrs are what every audit record of a host operation carries: the
// tenant, the agent that caused it, the ticket and what was done.
func (s *Service) attrs(tenant, agentID uuid.UUID, ticketID int64, op string, more ...any) []any {
	return append([]any{"tenant_id", tenant.String(), "agent_id", agentID.String(), "ticket", ticketID, "operation", op}, more...)
}
