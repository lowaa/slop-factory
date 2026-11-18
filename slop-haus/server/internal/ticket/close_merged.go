package ticket

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// A change merged on the code host by someone slop-haus does not act on (an
// unlinked account) leaves its ticket open. Close as merged is the owner
// deciding it is done anyway, on the host's word that the change really is
// merged into the ticket's base branch -- never on the webhook's, which only
// records what it ignored so the owner can see why.

var (
	// ErrNotTenantOwner refuses Close as merged to anyone who did not make
	// the tenant: members are not included.
	ErrNotTenantOwner = errors.New("ticket: only the workspace owner can close a ticket as merged")
	// ErrNotMergedIntoBase is a change the code host does not show as merged
	// into the ticket's base branch.
	ErrNotMergedIntoBase = errors.New("ticket: the change is not merged into the ticket's base branch on the code host")
	// ErrMergeUnverifiable is a ticket whose merge cannot be checked: it has
	// no change on a host slop-haus can read, or the host did not answer.
	ErrMergeUnverifiable = errors.New("ticket: cannot check the merge on the code host")
	// ErrTicketFinished is a ticket already done or cancelled.
	ErrTicketFinished = errors.New("ticket: the ticket is already finished")
)

// TenantOwners says who made a tenant. Implemented by internal/user.
type TenantOwners interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// UserNames names a person for the audit line. Implemented by
// internal/user's store; optional.
type UserNames interface {
	UserName(ctx context.Context, id uuid.UUID) (string, error)
}

// WithTenantOwners sets who may close a ticket as merged. Without it nobody
// may.
func WithTenantOwners(o TenantOwners) ServiceOption {
	return func(s *Service) { s.tenantOwners = o }
}

// WithUserNames sets how the person who accepted a merge is named in the
// ticket's record. Without it they are named by id.
func WithUserNames(n UserNames) ServiceOption {
	return func(s *Service) { s.userNames = n }
}

// IgnoredMerge is a merge of a ticket's change that the webhook did not act
// on.
type IgnoredMerge struct {
	TicketID int64
	Attempt  int
	PR       codehost.ChangeRef
	// MergeSHA is empty when the delivery did not name the merge commit.
	MergeSHA       string
	MergerLogin    string
	MergerGitHubID int64
	// Reason is why the webhook ignored the merge, in its own words.
	Reason string
	At     time.Time
}

// RecordIgnoredMerge keeps a merge of pr that the webhook ignored, against
// every live ticket whose promoted attempt reports pr. A PR that belongs to
// no live ticket records nothing.
func (s *Service) RecordIgnoredMerge(ctx context.Context, m *IgnoredMerge) error {
	finder, ok := s.store.(PRAttemptStore)
	if !ok {
		return errors.New("ticket: the store cannot find attempts by PR")
	}
	facts, ok := s.store.(MergeFactsStore)
	if !ok {
		return errors.New("ticket: the store cannot record ignored merges")
	}
	found, err := finder.AttemptsForPR(ctx, s.hosts.ChangeURL(m.PR))
	if err != nil {
		return err
	}
	for _, a := range found {
		if !a.Promoted || !linkIsPR(s.hosts, a.PRURL, m.PR) {
			continue
		}
		t, err := s.store.GetTicket(ctx, a.TicketID)
		if err != nil {
			return err
		}
		if t.Status.IsTerminal() {
			continue
		}
		rec := *m
		rec.TicketID, rec.Attempt = a.TicketID, a.Number
		if err := facts.RecordIgnoredMerge(ctx, &rec); err != nil {
			return err
		}
	}
	return nil
}

// CheckVerdict is where a merged head's checks stood.
type CheckVerdict string

const (
	ChecksPassed CheckVerdict = "passed"
	ChecksFailed CheckVerdict = "failed"
	// ChecksNotRun is no check having said anything that counts, or one
	// still pending: neither vouches for the head.
	ChecksNotRun CheckVerdict = "not_run"
)

// MergeFacts is what a person needs to decide a ticket's change was merged
// elsewhere, read from the code host now.
type MergeFacts struct {
	TicketID int64
	PR       codehost.ChangeRef
	// Base is the branch the ticket merges into.
	Base string
	// Ignored is the merge the webhook ignored, if one was recorded.
	Ignored *IgnoredMerge
	// Merged is whether the host shows the change merged at all, and
	// MergedIntoBase whether that was into Base: the only merge that counts.
	Merged         bool
	MergedIntoBase bool
	// MergedBy and MergeSHA are the host's, falling back to the recorded
	// merge's when the host does not say.
	MergedBy string
	MergeSHA string
	// MergedHead is the change's head commit. ApprovedHead is the commit an
	// approval covers, empty when there is none; HeadApproved is whether it
	// is MergedHead.
	MergedHead   string
	ApprovedHead string
	HeadApproved bool
	Checks       CheckVerdict
	// MergeOnBase is whether the merge commit is in Base's history now.
	MergeOnBase bool
}

// Confirmations are what the merge leaves a person to confirm before the
// ticket is closed: the code is on the base branch either way, so these
// inform rather than refuse.
func (f *MergeFacts) Confirmations() []string {
	var out []string
	if f.Checks != ChecksPassed {
		out = append(out, fmt.Sprintf("the checks on the merged head %s did not pass (%s)", short(f.MergedHead), f.Checks))
	}
	if !f.HeadApproved {
		if f.ApprovedHead == "" {
			out = append(out, fmt.Sprintf("the merged head %s was never approved", short(f.MergedHead)))
		} else {
			out = append(out, fmt.Sprintf("the merged head %s is not the approved head %s", short(f.MergedHead), short(f.ApprovedHead)))
		}
	}
	return out
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// MergeFacts reads the facts about a ticket's change for deciding whether it
// was merged elsewhere. It reads the code host, so the answer is as of now,
// and works whether or not a merge was ever recorded.
func (s *Service) MergeFacts(ctx context.Context, ticketID int64) (*MergeFacts, error) {
	t, err := s.store.GetTicket(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	return s.mergeFacts(ctx, t)
}

func (s *Service) mergeFacts(ctx context.Context, t *Ticket) (*MergeFacts, error) {
	facts, ok := s.store.(MergeFactsStore)
	if !ok {
		return nil, errors.New("ticket: the store cannot read merge facts")
	}
	if t.PRURL == nil {
		return nil, fmt.Errorf("%w: #%d has no change", ErrMergeUnverifiable, t.ID)
	}
	pr, ok := s.hosts.ParseChangeURL(*t.PRURL)
	if !ok {
		return nil, fmt.Errorf("%w: #%d's change is on a host slop-haus cannot read", ErrMergeUnverifiable, t.ID)
	}
	host, err := s.hostFor(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMergeUnverifiable, err)
	}
	// The base is the ticket's own, from the manifest, never the change's: a
	// change merged into some other branch is not the ticket's work landing.
	if host == nil || host.repo != pr.InRepo() {
		return nil, fmt.Errorf("%w: #%d's change is not in the repository its scopes are in", ErrMergeUnverifiable, t.ID)
	}
	rb, err := s.resolveBase(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMergeUnverifiable, err)
	}
	f := &MergeFacts{TicketID: t.ID, PR: pr, Base: rb.Branch}
	if f.Ignored, err = facts.LatestIgnoredMerge(ctx, t.ID, pr); err != nil {
		return nil, err
	}
	state, err := host.changes.ChangeState(ctx, host.tenant, pr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMergeUnverifiable, err)
	}
	f.Merged = state.Merged
	f.MergedIntoBase = state.Merged && state.Base == rb.Branch
	f.MergedBy, f.MergeSHA, f.MergedHead = state.MergedBy, state.MergeSHA, state.HeadSHA
	if f.Ignored != nil {
		if f.MergedBy == "" {
			f.MergedBy = f.Ignored.MergerLogin
		}
		if f.MergeSHA == "" {
			f.MergeSHA = f.Ignored.MergeSHA
		}
	}
	if !f.MergedIntoBase {
		return f, nil
	}
	if f.ApprovedHead, err = facts.ReviewedHead(ctx, t.ID, pr); err != nil {
		return nil, err
	}
	f.HeadApproved = f.ApprovedHead != "" && f.ApprovedHead == f.MergedHead
	checks, err := host.changes.Checks(ctx, host.tenant, pr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMergeUnverifiable, err)
	}
	switch {
	case checks.HeadSHA != f.MergedHead:
		f.Checks = ChecksNotRun
	case checks.Passed():
		f.Checks = ChecksPassed
	case checks.State == codehost.CheckFailure:
		f.Checks = ChecksFailed
	default:
		f.Checks = ChecksNotRun
	}
	if f.MergeSHA != "" {
		if f.MergeOnBase, err = host.changes.BranchContains(ctx, host.tenant, host.repo, rb.Branch, f.MergeSHA); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrMergeUnverifiable, err)
		}
	}
	return f, nil
}

// CloseAsMergedResult is what CloseAsMerged did. Exactly one of Ticket (it
// closed it) and NeedsConfirmation (it did not) is set.
type CloseAsMergedResult struct {
	Ticket *Ticket
	// NeedsConfirmation is set when the merge leaves something to confirm;
	// Confirmations says what, and the caller asks again with confirm.
	NeedsConfirmation bool
	Confirmations     []string
	Facts             *MergeFacts
}

// CloseAsMerged is the tenant owner closing a ticket as done because its
// change was merged outside slop-haus. The code host is asked whether the
// change is merged into the ticket's base branch, and the close is refused
// if it is not (ErrNotMergedIntoBase). A merge whose checks did not pass, or
// whose head was not the approved one, comes back as NeedsConfirmation until
// confirm is set. The ticket closes through the same transition a merge by a
// linked member does, with person as the actor, and any agent's lease and
// integration lock end with it.
func (s *Service) CloseAsMerged(ctx context.Context, ticketID int64, person uuid.UUID, confirm bool) (*CloseAsMergedResult, error) {
	tenant, err := s.requireTenantOwner(ctx, person)
	if err != nil {
		return nil, err
	}
	t, err := s.store.GetTicket(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	if t.Status.IsTerminal() {
		return nil, fmt.Errorf("%w: #%d is %s", ErrTicketFinished, t.ID, t.Status)
	}
	f, err := s.mergeFacts(ctx, t)
	if err != nil {
		return nil, err
	}
	if !f.MergedIntoBase {
		return nil, fmt.Errorf("%w: %s (base %s)", ErrNotMergedIntoBase, s.hosts.ChangeURL(f.PR), f.Base)
	}
	if reasons := f.Confirmations(); len(reasons) > 0 && !confirm {
		return &CloseAsMergedResult{NeedsConfirmation: true, Confirmations: reasons, Facts: f}, nil
	}
	body := fmt.Sprintf("merged by %s outside slop-haus at %s; accepted by %s", orUnknown(f.MergedBy), orUnknown(f.MergeSHA), s.personName(ctx, person))
	closed, err := s.transition(WithUser(ctx, person), t.ID, func(current *Ticket) (*Change, error) {
		if current.Status.IsTerminal() {
			return nil, fmt.Errorf("%w: #%d is %s", ErrTicketFinished, current.ID, current.Status)
		}
		// The facts were read for the change the ticket had then.
		if !hasPR(s.hosts, current, f.PR) {
			return nil, fmt.Errorf("%w: #%d's change is no longer %s", ErrNotMergeable, current.ID, s.hosts.ChangeURL(f.PR))
		}
		return &Change{
			Comment: &NewComment{By: UserAuthor(person), Body: body},
			Ticket:  TicketWrite{Status: To(StatusDone), Closing: Closes()},
			Unblock: true,
			Revoke:  true,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	if s.events != nil {
		s.events.Record(ctx, audit.TicketClosedAsMerged, "tenant", tenant, "user_id", person, "ticket", t.ID,
			"change", s.hosts.ChangeURL(f.PR), "merged_by", f.MergedBy, "merge_sha", f.MergeSHA,
			"head_sha", f.MergedHead, "confirmed", len(f.Confirmations()) > 0)
	}
	return &CloseAsMergedResult{Ticket: closed, Facts: f}, nil
}

// requireTenantOwner refuses anyone but the tenant's owner, before anything
// about the ticket is read, so a member learns nothing of it.
func (s *Service) requireTenantOwner(ctx context.Context, person uuid.UUID) (uuid.UUID, error) {
	if err := s.ownerGate.RequireOwnerInContext(ctx, person); err != nil {
		return uuid.Nil, err
	}
	return db.TenantFrom(ctx)
}

// MergeFactsForOwner is MergeFacts for the tenant owner deciding whether to
// close the ticket as merged; anyone else is refused.
func (s *Service) MergeFactsForOwner(ctx context.Context, ticketID int64, person uuid.UUID) (*MergeFacts, error) {
	if _, err := s.requireTenantOwner(ctx, person); err != nil {
		return nil, err
	}
	return s.MergeFacts(ctx, ticketID)
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

func (s *Service) personName(ctx context.Context, id uuid.UUID) string {
	if s.userNames != nil {
		if name, err := s.userNames.UserName(ctx, id); err == nil && name != "" {
			return name
		}
	}
	return id.String()
}
