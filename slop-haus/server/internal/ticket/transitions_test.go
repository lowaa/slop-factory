package ticket_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// These tests pin everything each state transition writes, read from the
// database rather than from what the call returns, through Service only.
// They are the contract for moving the transition rules out of the store:
// the store's own interface is free to change under them, and any column a
// transition stops writing -- or starts writing -- fails one of them.

// written is what the transitions write about one ticket.
type written struct {
	Status           ticket.Status
	NeedsDiscussion  bool
	ResumeAgentID    *uuid.UUID
	Affinity         bool
	Closed           bool
	PRURL            *string
	Approved         bool
	ApprovedBy       *uuid.UUID
	ChangesRequested bool
	ReviewStarted    bool
	// Lease is the ticket's most recent lease, nil if it never had one.
	Lease    *leaseState
	Comments []said
	Handoffs []string
	// Locks counts the locks held through any of the ticket's leases.
	Locks int
}

type leaseState struct {
	AgentID   uuid.UUID
	Released  bool
	Reason    *string
	RevokedTo *string
	Delivered bool
}

type said struct {
	AgentID *uuid.UUID
	UserID  *uuid.UUID
	Body    string
}

func (f *fixture) written(t *testing.T, id int64) written {
	t.Helper()
	var w written
	require.NoError(t, f.database.QueryRow(f.ctx, `
		SELECT t.status, t.needs_discussion, a.resume_agent_id, a.affinity_until IS NOT NULL,
		       t.closed_at IS NOT NULL, a.pr_url, a.approved_at IS NOT NULL, a.approved_by_user_id,
		       a.changes_requested_at IS NOT NULL, a.review_started_at IS NOT NULL
		  FROM tickets t JOIN attempts a ON a.tenant_id = t.tenant_id AND a.ticket_id = t.id AND a.number = 1
		 WHERE t.id = $1`, id).Scan(
		&w.Status, &w.NeedsDiscussion, &w.ResumeAgentID, &w.Affinity,
		&w.Closed, &w.PRURL, &w.Approved, &w.ApprovedBy,
		&w.ChangesRequested, &w.ReviewStarted))

	var l leaseState
	err := f.database.QueryRow(f.ctx, `
		SELECT agent_id, released_at IS NOT NULL, release_reason, revoked_to,
		       revocation_delivered_at IS NOT NULL
		  FROM leases WHERE ticket_id = $1 ORDER BY id DESC LIMIT 1`, id).Scan(
		&l.AgentID, &l.Released, &l.Reason, &l.RevokedTo, &l.Delivered)
	if !errors.Is(err, pgx.ErrNoRows) {
		require.NoError(t, err)
		w.Lease = &l
	}

	rows, err := f.database.Query(f.ctx, `
		SELECT author_agent_id, author_user_id, body FROM ticket_comments
		 WHERE ticket_id = $1 ORDER BY id`, id)
	require.NoError(t, err)
	w.Comments, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (said, error) {
		var s said
		return s, row.Scan(&s.AgentID, &s.UserID, &s.Body)
	})
	require.NoError(t, err)

	rows, err = f.database.Query(f.ctx,
		`SELECT body FROM agent_contexts WHERE ticket_id = $1 ORDER BY id`, id)
	require.NoError(t, err)
	w.Handoffs, err = pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	require.NoError(t, f.database.QueryRow(f.ctx, `
		SELECT count(*) FROM locks lk JOIN leases l ON l.id = lk.holder_lease_id
		 WHERE l.ticket_id = $1`, id).Scan(&w.Locks))
	f.assertHistoryFollowsStatus(t, id, w.Status)
	return w
}

// everything is every row a transition could touch, for a refusal to be
// checked against: a refused transition writes nothing at all.
func (f *fixture) everything(t *testing.T) string {
	t.Helper()
	var snapshot string
	require.NoError(t, f.database.QueryRow(f.ctx, `
		SELECT json_build_object(
			'tickets',  (SELECT json_agg(x ORDER BY x.id) FROM tickets x),
			'paths',    (SELECT json_agg(x ORDER BY x.ticket_id, x.path) FROM ticket_paths x),
			'deps',     (SELECT json_agg(x ORDER BY x.ticket_id, x.depends_on_ticket_id) FROM ticket_deps x),
			'leases',   (SELECT json_agg(x ORDER BY x.id) FROM leases x),
			'locks',    (SELECT json_agg(x ORDER BY x.name) FROM locks x),
			'comments', (SELECT json_agg(x ORDER BY x.id) FROM ticket_comments x),
			'handoffs', (SELECT json_agg(x ORDER BY x.id) FROM agent_contexts x)
		)::text`).Scan(&snapshot))
	return snapshot
}

func agentSaid(agentID uuid.UUID, body string) said { return said{AgentID: &agentID, Body: body} }
func humanSaid(userID uuid.UUID, body string) said  { return said{UserID: &userID, Body: body} }

// ended is a lease released for reason, not revoked.
func ended(agentID uuid.UUID, reason string) *leaseState {
	return &leaseState{AgentID: agentID, Released: true, Reason: &reason}
}

// revoked is a lease a human's move to status took away.
func revoked(agentID uuid.UUID, status ticket.Status, delivered bool) *leaseState {
	return &leaseState{
		AgentID: agentID, Released: true, Reason: ptr("revoked: moved to " + string(status) + " by a human"),
		RevokedTo: ptr(string(status)), Delivered: delivered,
	}
}

const earlierPR = "https://github.example/acme/repo/pull/7"

// heldExits are the agent's ways of giving up the ticket it holds.
var heldExits = []struct {
	name string
	run  func(f *fixture, a uuid.UUID) (*ticket.Ticket, error)
	// want is what the exit writes over the held ticket's state.
	want func(w *written, a uuid.UUID)
	// unblocks is whether a ticket waiting on the held one reopens.
	unblocks bool
	// gatedOnDiscussion is an exit refused while the ticket needs
	// discussion (see ErrNeedsDiscussion), so it starts from a ticket
	// that no longer does.
	gatedOnDiscussion bool
}{
	{
		name: "release",
		run: func(f *fixture, a uuid.UUID) (*ticket.Ticket, error) {
			return f.svc.Release(f.ctx, a, "giving it back", nil)
		},
		want: func(w *written, a uuid.UUID) {
			w.Status = ticket.StatusOpen
			w.Lease = ended(a, "giving it back")
			w.Comments = []said{agentSaid(a, "giving it back")}
		},
	},
	{
		name:     "done",
		unblocks: true, gatedOnDiscussion: true,
		run: func(f *fixture, a uuid.UUID) (*ticket.Ticket, error) {
			return f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "finished"})
		},
		want: func(w *written, a uuid.UUID) {
			w.Status = ticket.StatusDone
			w.Closed = true
			w.Comments = append(w.Comments, agentSaid(a, "finished"))
			w.Lease = ended(a, "done")
		},
	},
	{
		name:     "done with a PR and no summary",
		unblocks: true, gatedOnDiscussion: true,
		run: func(f *fixture, a uuid.UUID) (*ticket.Ticket, error) {
			return f.svc.Done(f.ctx, a, &ticket.DoneParams{PRURL: reviewPR})
		},
		want: func(w *written, a uuid.UUID) {
			w.Status = ticket.StatusDone
			w.Closed = true
			w.PRURL = ptr(reviewPR)
			w.Lease = ended(a, "done")
		},
	},
	{
		name: "discuss",
		run: func(f *fixture, a uuid.UUID) (*ticket.Ticket, error) {
			return f.svc.Discuss(f.ctx, a, "Which shape?", "Half-done on a branch.")
		},
		want: func(w *written, a uuid.UUID) {
			w.Status = ticket.StatusDiscussion
			w.NeedsDiscussion = false
			w.ResumeAgentID = &a
			w.Comments = append(w.Comments, agentSaid(a, "Which shape?"))
			w.Handoffs = append(w.Handoffs, "Half-done on a branch.")
			w.Lease = ended(a, "discussion")
		},
	},
	{
		name: "review", gatedOnDiscussion: true,
		run: func(f *fixture, a uuid.UUID) (*ticket.Ticket, error) {
			return f.svc.Review(f.ctx, a, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "PR open."})
		},
		want: func(w *written, a uuid.UUID) {
			w.Status = ticket.StatusReview
			w.PRURL = ptr(reviewPR)
			w.ResumeAgentID = &a
			w.Approved, w.ApprovedBy = false, nil
			w.ReviewStarted = true
			w.Comments = append(w.Comments, agentSaid(a, ticket.VoluntaryReviewLead+"reviewed by a person"))
			w.Handoffs = append(w.Handoffs, "PR open.")
			w.Lease = ended(a, "review")
		},
	},
	{
		name: "prepare",
		run: func(f *fixture, a uuid.UUID) (*ticket.Ticket, error) {
			return f.svc.Prepare(f.ctx, a, "Needs reshaping.")
		},
		want: func(w *written, a uuid.UUID) {
			w.Status = ticket.StatusPreparing
			w.Comments = append(w.Comments, agentSaid(a, "Needs reshaping."))
			w.Lease = ended(a, "preparing")
		},
	},
}

// Each exit starts from a ticket carrying state it may or may not touch --
// a planning request, an earlier PR and approval, the integration lock, a
// ticket waiting on it -- so both what it writes and what it leaves are
// pinned.
func TestTransition_HeldExits(t *testing.T) {
	t.Parallel()
	for _, exit := range heldExits {
		t.Run(exit.name, func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "builder-1")
			held := f.ticket(t, "Held", "server/")
			waiting := f.ticket(t, "Waits on the held one", "web/")
			f.blockedBy(t, waiting.ID, held.ID)
			needsDiscussion := !exit.gatedOnDiscussion
			_, err := f.database.Exec(f.ctx, `
				UPDATE attempts SET pr_url = $2, approved_at = now(), approved_by_user_id = $3
				 WHERE ticket_id = $1 AND number = 1`, held.ID, earlierPR, f.human)
			require.NoError(t, err)
			_, err = f.database.Exec(f.ctx, `UPDATE tickets SET needs_discussion = $2 WHERE id = $1`, held.ID, needsDiscussion)
			require.NoError(t, err)
			_, err = f.svc.Claim(f.ctx, a, &held.ID)
			require.NoError(t, err)
			require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))

			want := f.written(t, held.ID)
			require.Equal(t, written{
				Status: ticket.StatusInProgress, NeedsDiscussion: needsDiscussion, PRURL: ptr(earlierPR),
				Approved: true, ApprovedBy: &f.human,
				Lease:    &leaseState{AgentID: a},
				Comments: []said{}, Handoffs: []string{}, Locks: 1,
			}, want, "the starting state")

			returned, err := exit.run(f, a)
			require.NoError(t, err)

			exit.want(&want, a)
			want.Locks = 0
			got := f.written(t, held.ID)
			assert.Equal(t, want, got)
			assert.Equal(t, got.Status, returned.Status, "the ticket returned is the one written")

			wantWaiting := ticket.StatusBlocked
			if exit.unblocks {
				wantWaiting = ticket.StatusOpen
			}
			assert.Equal(t, wantWaiting, f.status(t, waiting.ID))
			assert.ErrorIs(t, f.svc.Heartbeat(f.ctx, a), ticket.ErrNoLease,
				"the lease is over, and nobody took it away")
		})
	}
}

// A release onto a blocker the ticket already waits on records no blocker
// comment, so the reason is posted on its own rather than lost.
func TestTransition_ReleaseBlockedOnExistingEdge(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	blocker := f.ticket(t, "Must land first")
	held := f.ticket(t, "Held", "server/")
	_, err := f.svc.Claim(f.ctx, a, &held.ID)
	require.NoError(t, err)
	f.blockedBy(t, held.ID, blocker.ID)

	_, err = f.svc.Release(f.ctx, a, "still needs the other", &blocker.ID)
	require.NoError(t, err)

	comments := f.written(t, held.ID).Comments
	require.Len(t, comments, 2, "the helper's blocker comment, then the reason")
	assert.Equal(t, agentSaid(a, "still needs the other"), comments[1])
}

// A held ticket released with blocked_on names the blocker in the agent's
// own words, and lands blocked.
func TestTransition_ReleaseBlockedOn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	blocker := f.ticket(t, "Must land first")
	held := f.ticket(t, "Held", "server/")
	_, err := f.svc.Claim(f.ctx, a, &held.ID)
	require.NoError(t, err)

	_, err = f.svc.Release(f.ctx, a, "needs the other", &blocker.ID)
	require.NoError(t, err)

	assert.Equal(t, written{
		Status:   ticket.StatusBlocked,
		Lease:    ended(a, "needs the other"),
		Comments: []said{agentSaid(a, fmt.Sprintf("Blocked on #%d: Must land first\n\nneeds the other", blocker.ID))},
		Handoffs: []string{},
	}, f.written(t, held.ID))
}

// humanOutcomes are a person ending a review or a discussion, each of which
// puts the ticket back in the queue with its agent holding first claim.
var humanOutcomes = []struct {
	name string
	// into puts the claimed ticket where the outcome applies.
	into func(t *testing.T, f *fixture, a uuid.UUID)
	run  func(f *fixture, id int64) error
	want func(w *written, f *fixture)
}{
	{
		name: "approve",
		into: func(t *testing.T, f *fixture, a uuid.UUID) { f.review(t, a) },
		run: func(f *fixture, id int64) error {
			_, err := f.svc.Approve(f.ctx, id, f.human)
			return err
		},
		want: func(w *written, f *fixture) {
			w.Approved, w.ApprovedBy = true, &f.human
		},
	},
	{
		name: "request changes",
		into: func(t *testing.T, f *fixture, a uuid.UUID) { f.review(t, a) },
		run: func(f *fixture, id int64) error {
			_, err := f.svc.RequestChanges(f.ctx, id, f.human, "Split it.")
			return err
		},
		want: func(w *written, f *fixture) {
			w.ChangesRequested = true
			w.Comments = append(w.Comments, humanSaid(f.human, "Split it."))
		},
	},
	{
		name: "answer a discussion",
		into: func(t *testing.T, f *fixture, a uuid.UUID) { f.discuss(t, a) },
		run: func(f *fixture, id int64) error {
			_, err := f.svc.AddComment(f.ctx, id, ticket.UserAuthor(f.human), "Flat.")
			return err
		},
		want: func(w *written, f *fixture) {
			w.Comments = append(w.Comments, humanSaid(f.human, "Flat."))
		},
	},
}

func TestTransition_HumanOutcomesRequeueWithFirstClaim(t *testing.T) {
	t.Parallel()
	for _, outcome := range humanOutcomes {
		for _, pending := range []bool{false, true} {
			name := outcome.name
			if pending {
				name += " with a pending dependency"
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				a := f.agent(t, "builder-1")
				held := f.ticket(t, "Held", "server/")
				waiting := f.ticket(t, "Waits on the held one", "web/")
				f.blockedBy(t, waiting.ID, held.ID)
				_, err := f.svc.Claim(f.ctx, a, &held.ID)
				require.NoError(t, err)
				outcome.into(t, f, a)
				if pending {
					f.blockedBy(t, held.ID, f.ticket(t, "Still open").ID)
				}

				want := f.written(t, held.ID)
				require.NoError(t, outcome.run(f, held.ID))

				outcome.want(&want, f)
				want.Status = ticket.StatusOpen
				if pending {
					want.Status = ticket.StatusBlocked
				}
				want.Affinity = true
				assert.Equal(t, want, f.written(t, held.ID))
				assert.Equal(t, &a, want.ResumeAgentID, "the agent that handed it over keeps first claim")
				assert.Equal(t, ticket.StatusBlocked, f.status(t, waiting.ID),
					"nothing the ticket blocks is finished by it going back to the queue")
			})
		}
	}
}

// A claim ends any first-claim hold, whoever makes it.
func TestTransition_ClaimClearsFirstClaim(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	held := f.ticket(t, "Held", "server/")
	f.claim(t, a)
	f.discuss(t, a)
	f.humanReply(t, held.ID)
	want := f.written(t, held.ID)
	require.True(t, want.Affinity)

	f.claim(t, a)

	want.Status = ticket.StatusInProgress
	want.ResumeAgentID, want.Affinity = nil, false
	want.Lease = &leaseState{AgentID: a}
	assert.Equal(t, want, f.written(t, held.ID))
}

// A person taking a held ticket back ends its lease as a revocation, which
// the agent is told about once, on its next call.
func TestTransition_HumanTakesAHeldTicketBack(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		run   func(t *testing.T, f *fixture, id int64) error
		to    ticket.Status
		want  func(w *written, f *fixture)
		frees bool
	}{
		{
			name: "cancel it",
			run: func(t *testing.T, f *fixture, id int64) error {
				_, err := f.setStatus(t, id, ticket.StatusCancelled)
				return err
			},
			to:    ticket.StatusCancelled,
			want:  func(w *written, _ *fixture) { w.Closed = true },
			frees: true,
		},
		{
			name: "ask for a planning pass",
			run: func(_ *testing.T, f *fixture, id int64) error {
				_, err := f.svc.CommentSettingNeedsDiscussion(f.ctx, id, f.human, "Plan first.", true)
				return err
			},
			to: ticket.StatusOpen,
			want: func(w *written, f *fixture) {
				w.NeedsDiscussion = true
				w.Comments = append(w.Comments, humanSaid(f.human, "Plan first."))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "builder-1")
			held := f.ticket(t, "Held", "server/")
			waiting := f.ticket(t, "Waits on the held one", "web/")
			f.blockedBy(t, waiting.ID, held.ID)
			f.claim(t, a)
			require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))
			want := f.written(t, held.ID)

			require.NoError(t, tc.run(t, f, held.ID))

			tc.want(&want, f)
			want.Status = tc.to
			want.Lease = revoked(a, tc.to, false)
			want.Locks = 0
			assert.Equal(t, want, f.written(t, held.ID))
			wantWaiting := ticket.StatusBlocked
			if tc.frees {
				wantWaiting = ticket.StatusOpen
			}
			assert.Equal(t, wantWaiting, f.status(t, waiting.ID))

			var notice *ticket.LeaseRevokedError
			require.ErrorAs(t, f.svc.Heartbeat(f.ctx, a), &notice)
			assert.Equal(t, ticket.LeaseRevokedError{TicketID: held.ID, Attempt: 1, Status: tc.to}, *notice)
			want.Lease.Delivered = true
			assert.Equal(t, want, f.written(t, held.ID))
			assert.ErrorIs(t, f.svc.Heartbeat(f.ctx, a), ticket.ErrNoLease, "told once")
		})
	}
}

// A person merging the PR on GitHub finishes the ticket as the agent's
// ticket_finish would, with the merge as their comment.
func TestTransition_CloseMergedPR(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	reviewed := f.inReviewWith(t, a, "Widgets", githubRefLink)
	waiting := f.ticket(t, "Waits on the widgets", "web/")
	f.blockedBy(t, waiting.ID, reviewed.ID)
	want := f.written(t, reviewed.ID)

	_, err := f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
	require.NoError(t, err)

	want.Status = ticket.StatusDone
	want.Closed = true
	want.Comments = append(want.Comments, humanSaid(f.human, "Merged on github.com: https://github.com/acme/widgets/pull/12"))
	assert.Equal(t, want, f.written(t, reviewed.ID))
	assert.Equal(t, ticket.StatusOpen, f.status(t, waiting.ID))
}

// The lease-ending routes nobody chose -- expiry, and an agent being
// disabled -- requeue the ticket as a release would, blocked while it waits
// on anything, and are not revocations.
func TestTransition_LeaseEndsWithoutTheAgent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		end    func(t *testing.T, f *fixture, a uuid.UUID)
		reason string
	}{
		{
			name: "expiry",
			end: func(t *testing.T, f *fixture, a uuid.UUID) {
				f.expire(t, a)
				other := f.ticket(t, "Elsewhere", "docs/")
				_, err := f.svc.Claim(f.ctx, f.agent(t, "builder-2"), &other.ID)
				require.NoError(t, err)
			},
			reason: "expired",
		},
		{
			name: "the agent disabled",
			end: func(t *testing.T, f *fixture, a uuid.UUID) {
				require.NoError(t, f.svc.ReleaseLeasesFor(f.ctx, a, "agent disabled", time.Now().Add(time.Hour)))
			},
			reason: "agent disabled",
		},
	} {
		for _, pending := range []bool{false, true} {
			name := tc.name
			if pending {
				name += " with a pending dependency"
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				a := f.agent(t, "builder-1")
				held := f.ticket(t, "Held", "server/")
				f.claim(t, a)
				if pending {
					f.blockedBy(t, held.ID, f.ticket(t, "Still open").ID)
				}
				want := f.written(t, held.ID)

				tc.end(t, f, a)

				want.Status = ticket.StatusOpen
				if pending {
					want.Status = ticket.StatusBlocked
				}
				want.Lease = ended(a, tc.reason)
				assert.Equal(t, want, f.written(t, held.ID))
			})
		}
	}
}

// Every refusal leaves every row as it was: the rule is judged before, or
// atomically with, anything the transition would write.
func TestTransition_RefusalsWriteNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// arrange builds the state and returns the refused call.
		arrange func(t *testing.T, f *fixture) func() error
		err     error
	}{
		{
			name: "done on an unapproved gated ticket",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				f.gated(t, "Gated", "server/")
				f.claim(t, a)
				return func() error {
					_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "finished"})
					return err
				}
			},
			err: ticket.ErrNotApproved,
		},
		{
			name: "done with a bad PR link",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				f.ticket(t, "Held", "server/")
				f.claim(t, a)
				return func() error {
					_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "finished", PRURL: "http://insecure"})
					return err
				}
			},
			err: ticket.ErrInvalidPRURL,
		},
		{
			name: "done without a lease",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				f.ticket(t, "Nobody's", "server/")
				return func() error {
					_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "finished"})
					return err
				}
			},
			err: ticket.ErrNoLease,
		},
		{
			name: "discuss without a handoff",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				f.ticket(t, "Held", "server/")
				f.claim(t, a)
				return func() error {
					_, err := f.svc.Discuss(f.ctx, a, "Which?", " ")
					return err
				}
			},
			err: ticket.ErrEmptyHandoff,
		},
		{
			name: "review without a PR",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				f.ticket(t, "Held", "server/")
				f.claim(t, a)
				return func() error {
					_, err := f.svc.Review(f.ctx, a, &ticket.ReviewParams{Reason: "reviewed by a person", Handoff: "PR open."})
					return err
				}
			},
			err: ticket.ErrPRURLRequired,
		},
		{
			name: "prepare without a reason",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				f.ticket(t, "Held", "server/")
				f.claim(t, a)
				return func() error {
					_, err := f.svc.Prepare(f.ctx, a, "")
					return err
				}
			},
			err: ticket.ErrEmptyReason,
		},
		{
			name: "release onto a cycle",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				first := f.ticket(t, "First", "server/one/")
				second := f.ticket(t, "Second", "server/two/")
				f.blockedBy(t, first.ID, second.ID)
				_, err := f.svc.Claim(f.ctx, a, &second.ID)
				require.NoError(t, err)
				return func() error {
					_, err := f.svc.Release(f.ctx, a, "needs the first", &first.ID)
					return err
				}
			},
			err: ticket.ErrDependencyCycle,
		},
		{
			name: "release onto a missing ticket",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				f.ticket(t, "Held", "server/")
				f.claim(t, a)
				return func() error {
					_, err := f.svc.Release(f.ctx, a, "needs a ghost", ptr(int64(9999)))
					return err
				}
			},
			err: ticket.ErrNotFound,
		},
		{
			name: "approve a ticket not in review",
			arrange: func(t *testing.T, f *fixture) func() error {
				open := f.ticket(t, "Open")
				return func() error {
					_, err := f.svc.Approve(f.ctx, open.ID, f.human)
					return err
				}
			},
			err: ticket.ErrNotInReview,
		},
		{
			name: "approve with a review from an earlier cycle",
			arrange: func(t *testing.T, f *fixture) func() error {
				reviewed := f.inReviewWith(t, f.agent(t, "builder-1"), "Widgets", githubRefLink)
				stale := f.prReview(t, reviewed.ID)
				stale.SubmittedAt = f.reviewStartedAt(t, reviewed.ID).Add(-2 * ticket.DefaultResumeAffinity)
				return func() error {
					_, err := f.svc.ApprovePR(f.ctx, stale)
					return err
				}
			},
			err: ticket.ErrStaleReview,
		},
		{
			name: "request changes without a comment",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				held := f.ticket(t, "Held", "server/")
				f.claim(t, a)
				f.review(t, a)
				return func() error {
					_, err := f.svc.RequestChanges(f.ctx, held.ID, f.human, "")
					return err
				}
			},
			err: ticket.ErrEmptyComment,
		},
		{
			name: "close a merge of a ticket an agent holds",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				reviewed := f.inReviewWith(t, a, "Widgets", githubRefLink)
				f.approve(t, reviewed.ID)
				_, err := f.svc.Claim(f.ctx, a, &reviewed.ID)
				require.NoError(t, err)
				return func() error {
					_, err := f.svc.CloseMergedPR(f.ctx, githubRef, f.human)
					return err
				}
			},
			err: ticket.ErrNotMergeable,
		},
		{
			name: "ready on a ticket not preparing",
			arrange: func(t *testing.T, f *fixture) func() error {
				open := f.ticket(t, "Open")
				return func() error {
					_, err := f.svc.MarkReady(f.ctx, open.ID)
					return err
				}
			},
			err: ticket.ErrNotPreparing,
		},
		{
			name: "move a ticket in review back to the queue",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				held := f.ticket(t, "Held", "server/")
				f.claim(t, a)
				f.review(t, a)
				return func() error {
					_, err := f.setStatus(t, held.ID, ticket.StatusOpen)
					return err
				}
			},
			err: ticket.ErrLeaveReview,
		},
		{
			name: "a bulk move with one refusal",
			arrange: func(t *testing.T, f *fixture) func() error {
				a := f.agent(t, "builder-1")
				held := f.ticket(t, "Held", "server/")
				waiting := f.ticket(t, "Waits on the held one", "web/")
				f.blockedBy(t, waiting.ID, held.ID)
				f.claim(t, a)
				prep := f.preparing(t, "Preparing")
				return func() error {
					_, err := f.svc.MoveMany(f.ctx, []int64{held.ID, prep.ID}, ticket.StatusOpen)
					return err
				}
			},
			err: ticket.ErrLeavePreparing,
		},
		{
			name: "a blocker that would close a cycle",
			arrange: func(t *testing.T, f *fixture) func() error {
				first, second := f.ticket(t, "First"), f.ticket(t, "Second")
				f.blockedBy(t, first.ID, second.ID)
				return func() error {
					_, err := f.svc.AddDependency(f.ctx, ticket.UserAuthor(f.human), second.ID, first.ID)
					return err
				}
			},
			err: ticket.ErrDependencyCycle,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			refused := tc.arrange(t, f)
			before := f.everything(t)

			require.ErrorIs(t, refused(), tc.err)

			assert.JSONEq(t, before, f.everything(t))
		})
	}
}
