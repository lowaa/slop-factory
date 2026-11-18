package stewardwire

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The comment conventions the readers parse. Both are lines of a ticket
// comment, so they show on the board and need no schema.
const (
	// HumanOnlyMarker, on a line of the agent comment that opened a
	// discussion, reserves that discussion for a human. Anyone able to comment
	// can only withhold the steward by writing it, never grant it.
	HumanOnlyMarker = "[human-only]"
)

// Changes is the part of the change store the readers use.
type Changes interface {
	Current(ctx context.Context, ticketID int64, attempt int) (*change.Record, error)
}

// ChangeTickets is the part of the ticket service the change readers use.
type ChangeTickets interface {
	Attempts(ctx context.Context, ticketID int64) ([]ticket.Attempt, error)
}

// ChangeReader reads the ticket's change from the code host, with the
// tenant's credential, as the server sees it now. It implements
// steward.CIReader, DiffReader and HeadReader.
type ChangeReader struct {
	hosts   *codehost.Registry
	changes Changes
	tickets ChangeTickets
}

// liveAttempt is the attempt of the ticket that is being worked: the one with
// a lease, else the promoted one. More than one candidate is an error, since
// guessing which attempt a verdict is about is how a check passes on the
// wrong one. Every reader of a change's facts resolves the attempt here, so
// they all read the same one.
func liveAttempt(ctx context.Context, tickets ChangeTickets, ticketID int64) (int, error) {
	attempts, err := tickets.Attempts(ctx, ticketID)
	if err != nil {
		return 0, fmt.Errorf("read attempts of ticket %d: %w", ticketID, err)
	}
	var held, promoted []int
	for i := range attempts {
		a := &attempts[i]
		if a.Holder != nil {
			held = append(held, a.Number)
		}
		if a.PromotedAt != nil && !terminal(a.Status) {
			promoted = append(promoted, a.Number)
		}
	}
	candidates := held
	if len(candidates) == 0 {
		candidates = promoted
	}
	if len(candidates) != 1 {
		return 0, fmt.Errorf("ticket %d has %d candidate attempts for a change, want one", ticketID, len(candidates))
	}
	return candidates[0], nil
}

func NewChangeReader(hosts *codehost.Registry, changes Changes, tickets ChangeTickets) *ChangeReader {
	return &ChangeReader{hosts: hosts, changes: changes, tickets: tickets}
}

// live finds the change of the attempt that is being worked (liveAttempt).
func (r *ChangeReader) live(ctx context.Context, ticketID int64) (*change.Record, codehost.Changes, uuid.UUID, error) {
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, nil, uuid.Nil, err
	}
	attempt, err := liveAttempt(ctx, r.tickets, ticketID)
	if err != nil {
		return nil, nil, uuid.Nil, err
	}
	rec, err := r.changes.Current(ctx, ticketID, attempt)
	if err != nil {
		return nil, nil, uuid.Nil, fmt.Errorf("read change of ticket %d: %w", ticketID, err)
	}
	host, ok := r.hosts.Changes(rec.Change.Host)
	if !ok {
		return nil, nil, uuid.Nil, fmt.Errorf("%w: %s", change.ErrNoHost, rec.Change.Host)
	}
	return rec, host, tenant, nil
}

func (r *ChangeReader) checks(ctx context.Context, ticketID int64) (*codehost.Checks, *change.Record, codehost.Changes, uuid.UUID, error) {
	rec, host, tenant, err := r.live(ctx, ticketID)
	if err != nil {
		return nil, nil, nil, uuid.Nil, err
	}
	checks, err := host.Checks(ctx, tenant, rec.Change)
	if err != nil {
		return nil, nil, nil, uuid.Nil, fmt.Errorf("read checks of ticket %d: %w", ticketID, err)
	}
	return checks, rec, host, tenant, nil
}

// ChecksPassed says whether every check passed on headSHA, which must be the
// change's head on the host now: checks for another commit are not a pass.
func (r *ChangeReader) ChecksPassed(ctx context.Context, ticketID int64, headSHA string) (bool, string, error) {
	checks, rec, _, _, err := r.checks(ctx, ticketID)
	if err != nil {
		return false, "", err
	}
	ref := r.hosts.ChangeURL(rec.Change)
	if checks.HeadSHA == "" || !strings.EqualFold(checks.HeadSHA, headSHA) {
		return false, ref, nil
	}
	return checks.Passed(), ref, nil
}

// CurrentHead is the head of the change on the host now.
func (r *ChangeReader) CurrentHead(ctx context.Context, ticketID int64) (string, error) {
	checks, _, _, _, err := r.checks(ctx, ticketID)
	if err != nil {
		return "", err
	}
	if checks.HeadSHA == "" {
		return "", errors.New("the host named no head commit")
	}
	return checks.HeadSHA, nil
}

// ChangedPaths are the files the change brings to its base, for the head it
// has now. A diff the host cannot hand over whole is an error, never "no
// paths".
func (r *ChangeReader) ChangedPaths(ctx context.Context, ticketID int64) ([]string, error) {
	checks, rec, host, tenant, err := r.checks(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	diff, err := host.Diff(ctx, tenant, rec.Change, checks.HeadSHA)
	if err != nil {
		return nil, fmt.Errorf("read diff of ticket %d: %w", ticketID, err)
	}
	paths := make([]string, len(diff.Files))
	for i, f := range diff.Files {
		paths[i] = f.Path
	}
	return paths, nil
}

// Comments lists a ticket's comments, oldest first.
type Comments interface {
	Attempts(ctx context.Context, ticketID int64) ([]ticket.Attempt, error)
	Get(ctx context.Context, id int64) (*ticket.Ticket, error)
	ListComments(ctx context.Context, ticketID int64) ([]ticket.Comment, error)
}

// CommentReader reads the conventions in a ticket's comments. It implements
// steward.MarkerReader.
type CommentReader struct {
	tickets Comments
}

func NewCommentReader(tickets Comments) *CommentReader {
	return &CommentReader{tickets: tickets}
}

// HumanOnly reads the discussion or review the ticket is waiting in: the newest agent
// comment is the question, and it is reserved when a line of it is the
// marker. It fails closed: a ticket not in discussion, or one with no agent
// comment to read, is reported as reserved.
func (r *CommentReader) HumanOnly(ctx context.Context, ticketID int64) (bool, string, error) {
	single, err := r.singleAttempt(ctx, ticketID)
	if err != nil {
		return false, "", err
	}
	if !single {
		return true, "", nil
	}
	t, err := r.tickets.Get(ctx, ticketID)
	if err != nil {
		return false, "", fmt.Errorf("read ticket %d: %w", ticketID, err)
	}
	// A review's reason and a discussion's question are read alike, so the
	// marker reserves either for a human.
	if t.Status != ticket.StatusDiscussion && t.Status != ticket.StatusReview {
		return true, "", nil
	}
	comments, err := r.tickets.ListComments(ctx, ticketID)
	if err != nil {
		return false, "", fmt.Errorf("read comments of ticket %d: %w", ticketID, err)
	}
	c := newestAgentComment(comments)
	if c == nil {
		return true, "", nil
	}
	return hasLine(c.Body, HumanOnlyMarker), commentRef(c), nil
}

// singleAttempt is whether the ticket has at most one live attempt. Comments
// are shared by a ticket's attempts and carry no mark of which agent the
// newest one is about, so with siblings running the readers cannot tell whose
// question or stop they are reading and decline to.
func (r *CommentReader) singleAttempt(ctx context.Context, ticketID int64) (bool, error) {
	attempts, err := r.tickets.Attempts(ctx, ticketID)
	if err != nil {
		return false, fmt.Errorf("read attempts of ticket %d: %w", ticketID, err)
	}
	live := 0
	for i := range attempts {
		if !terminal(attempts[i].Status) {
			live++
		}
	}
	return live <= 1, nil
}

func terminal(status string) bool { return status == "done" || status == "closed" }

func newestAgentComment(comments []ticket.Comment) *ticket.Comment {
	for i := len(comments) - 1; i >= 0; i-- {
		if comments[i].AuthorAgentID != nil {
			return &comments[i]
		}
	}
	return nil
}

func commentRef(c *ticket.Comment) string { return fmt.Sprintf("comment %d", c.ID) }

func hasLine(body, line string) bool {
	for _, l := range strings.Split(body, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

// Proposals lists proposals.
type Proposals interface {
	List(ctx context.Context, filter *pendingaction.ListFilter) ([]pendingaction.Action, error)
}

// planScan is how many of the newest approved answers are looked through.
const planScan = 50

// PlanReader finds an approved plan: a discussion answer a person approved on
// the ticket by a decision of their own, not their auto-approve setting. Any
// such answer counts, however old: the bookkeeping it licenses is held to the
// ticket's revision and scope by the other checks. It implements steward.PlanReader.
type PlanReader struct{ proposals Proposals }

func NewPlanReader(proposals Proposals) *PlanReader { return &PlanReader{proposals: proposals} }

func (r *PlanReader) ApprovedPlan(ctx context.Context, ticketID int64) (bool, string, error) {
	kind := pendingaction.KindDiscussionAnswer
	found, err := r.proposals.List(ctx, &pendingaction.ListFilter{
		TicketID: &ticketID, Kind: &kind, States: []pendingaction.State{pendingaction.StateApproved}, Limit: planScan,
	})
	if err != nil {
		return false, "", fmt.Errorf("list approved answers of ticket %d: %w", ticketID, err)
	}
	for i := range found {
		if found[i].DecidedByUserID != nil && !found[i].AutoApproved() {
			return true, "proposal " + found[i].ID.String(), nil
		}
	}
	return false, "", nil
}

var (
	_ steward.CIReader     = (*ChangeReader)(nil)
	_ steward.DiffReader   = (*ChangeReader)(nil)
	_ steward.HeadReader   = (*ChangeReader)(nil)
	_ steward.MarkerReader = (*CommentReader)(nil)
	_ steward.PlanReader   = (*PlanReader)(nil)
)
