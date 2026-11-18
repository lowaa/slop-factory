package ticket

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
)

// plainRelease is a release under no cooldown policy: the exit as it was
// before cooldowns, which the cases below pin.
func plainRelease(reason, handoff string, blockedOn *int64) heldExit {
	return releaseExit(&ReleaseParams{Reason: reason, Handoff: handoff, BlockedOn: blockedOn},
		CooldownPolicy{}, CooldownStreak{}, time.Time{}, &ReleaseOutcome{})
}

// Each held exit's whole decision, with no database: what it writes to the
// ticket's row and what goes with it. transitions_test.go shows the store
// persisting these; this pins the decisions themselves.
func TestHeldExits(t *testing.T) {
	t.Parallel()
	agent := uuid.New()
	pr := "https://example.test/org/repo/pull/7"
	blocker := int64(4)
	reason := func(r string) *string { return &r }

	cases := []struct {
		name string
		exit heldExit
		want *Change
	}{
		{"release", plainRelease("out of time", "", nil), &Change{
			Comment:       &NewComment{By: AgentAuthor(agent), Body: "out of time"},
			ReleaseReason: reason("out of time"),
			Ticket:        TicketWrite{Status: ToQueue()},
		}},
		{"release blocked on", plainRelease("waiting", "", &blocker), &Change{
			ReleaseReason: reason("waiting"),
			BlockedOn:     &blocker,
			BlockedNote:   "waiting",
			Ticket:        TicketWrite{Status: ToQueue()},
		}},
		{"release with handoff, blank reason", plainRelease("  ", "branch x", nil), &Change{
			Handoff:       "branch x",
			ReleaseReason: reason("  "),
			Ticket:        TicketWrite{Status: ToQueue()},
		}},
		{"done", doneExit("shipped", &pr), &Change{
			Comment:       &NewComment{By: AgentAuthor(agent), Body: "shipped"},
			ReleaseReason: reason("done"),
			Ticket:        TicketWrite{Status: To(StatusDone), Closing: Closes(), PRURL: &pr},
			Unblock:       true,
		}},
		// No summary, no comment; no link, the earlier one is kept.
		{"done bare", doneExit("", nil), &Change{
			ReleaseReason: reason("done"),
			Ticket:        TicketWrite{Status: To(StatusDone), Closing: Closes()},
			Unblock:       true,
		}},
		{"discuss", discussExit("Which shape?", "half done"), &Change{
			Comment:       &NewComment{By: AgentAuthor(agent), Body: "Which shape?"},
			Handoff:       "half done",
			ReleaseReason: reason("discussion"),
			Ticket: TicketWrite{
				Status: To(StatusDiscussion), Discussed: true, FirstClaim: FirstClaimOwedTo(agent),
			},
		}},
		{"review", reviewExit(pr, "PR open", ""), &Change{
			Handoff:       "PR open",
			ReleaseReason: reason("review"),
			Ticket: TicketWrite{
				Status: To(StatusReview), PRURL: &pr, FirstClaim: FirstClaimOwedTo(agent), Review: ReviewStarts(),
			},
		}},
		{"prepare", prepareExit("reshape it"), &Change{
			Comment:       &NewComment{By: AgentAuthor(agent), Body: "reshape it"},
			ReleaseReason: reason("preparing"),
			Ticket:        TicketWrite{Status: To(StatusPreparing), FirstClaim: FirstClaimCleared()},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.exit(&Ticket{ID: 12, Status: StatusInProgress}, agent)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// Only an approval lets a gated ticket be done; a ticket that never needed
// one is done whether or not it has one.
func TestDoneExit_ApprovalGate(t *testing.T) {
	t.Parallel()
	approvedAt := time.Now()
	cases := []struct {
		name     string
		held     Ticket
		refusing bool
	}{
		{"not gated", Ticket{ID: 12}, false},
		{"gated, unapproved", Ticket{ID: 12, RequiresApproval: true}, true},
		{"gated, approved", Ticket{ID: 12, RequiresApproval: true, ApprovedAt: &approvedAt}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			change, err := doneExit("shipped", nil)(&tc.held, uuid.New())
			if !tc.refusing {
				require.NoError(t, err)
				assert.NotNil(t, change)
				return
			}
			require.ErrorIs(t, err, ErrNotApproved)
			assert.Contains(t, err.Error(), "#12", "the refusal names the ticket")
			assert.Nil(t, change)
		})
	}
}

// Done and review refuse a ticket that still needs discussion, whatever its
// review state -- discussion outranks even an approval; the other exits
// stay open, so the agent always has a way out.
func TestHeldExits_NeedsDiscussionGate(t *testing.T) {
	t.Parallel()
	approvedAt := time.Now()
	pr := "https://example.test/org/repo/pull/7"
	gatedExits := map[string]heldExit{
		"done":   doneExit("shipped", &pr),
		"review": reviewExit(pr, "PR open", ""),
	}
	openExits := map[string]heldExit{
		"release": plainRelease("out of time", "", nil),
		"discuss": discussExit("Which shape?", "plan"),
		"prepare": prepareExit("reshape it"),
	}
	for _, held := range map[string]Ticket{
		"ungated":           {ID: 12, NeedsDiscussion: true},
		"gated, unapproved": {ID: 12, NeedsDiscussion: true, RequiresApproval: true},
		"gated, approved":   {ID: 12, NeedsDiscussion: true, RequiresApproval: true, ApprovedAt: &approvedAt},
	} {
		for name, exit := range gatedExits {
			t.Run(name, func(t *testing.T) {
				change, err := exit(&held, uuid.New())
				require.ErrorIs(t, err, ErrNeedsDiscussion)
				assert.Contains(t, err.Error(), "#12", "the refusal names the ticket")
				assert.Contains(t, err.Error(), "ticket_discuss", "the refusal names the way out")
				assert.Nil(t, change)
			})
		}
		for name, exit := range openExits {
			t.Run(name, func(t *testing.T) {
				change, err := exit(&held, uuid.New())
				require.NoError(t, err)
				assert.NotNil(t, change)
			})
		}
	}
}

// A first claim's write is owed to an agent with no window open yet,
// cleared, a window opened for whoever it is owed to, or left alone; a
// review's is its cycle starting, or one of its two outcomes.
// discussionRoundSet is the clause that opens a discussion round when the
// status written is discussion and the attempt's was not.
const discussionRoundSet = "discussion_round = discussion_round + CASE WHEN status <> 'discussion' AND attempt_status_for_ticket($2::text) = 'discussion' THEN 1 ELSE 0 END"

func TestTicketWriteSets(t *testing.T) {
	t.Parallel()
	agent := uuid.New()
	pr := "https://example.test/pull/1"
	cleared := false

	ticket, attempt := ticketWriteSets(&TicketWrite{})
	assert.Equal(t, []string{"updated_at = now()"}, ticket.clauses, "the zero write moves updated_at alone")
	assert.Empty(t, ticket.args)
	assert.Equal(t, []string{"updated_at = now()"}, attempt.clauses, "and the attempt's too, which shows when the ticket was last written")

	ticket, attempt = ticketWriteSets(&TicketWrite{
		Status: To(StatusReview), NeedsDiscussion: &cleared, PRURL: &pr,
		FirstClaim: FirstClaimOwedTo(agent), Review: ReviewStarts(),
	})
	assert.Equal(t, []string{"updated_at = now()", "blocked_until = NULL", "cooldown_releases = 0", "cooldown_since = NULL", "needs_discussion = $2"}, ticket.clauses,
		"the ticket's status is worked out from its attempts, so it is not written here")
	assert.Equal(t, []any{false}, ticket.args)
	assert.Equal(t, concat(
		[]string{"updated_at = now()", discussionRoundSet, "status = attempt_status_for_ticket($2::text)", "pr_url = $3", "resume_agent_id = $4", "affinity_until = NULL"},
		reviewOutcomeCleared, []string{reviewStartedNow}), attempt.clauses,
		"the attempt takes the status, and the moved fields are its own, numbered after the ticket")
	assert.Equal(t, []any{"review", pr, agent}, attempt.args)

	ticket, attempt = ticketWriteSets(&TicketWrite{Status: ToQueue(), Closing: Closes(), FirstClaim: FirstClaimCleared()})
	assert.Equal(t, []string{"updated_at = now()", "blocked_until = NULL", "cooldown_releases = 0", "cooldown_since = NULL"}, ticket.clauses,
		"a requeue ends any cooldown before the status is worked out from the attempts, and closed_at with it")
	assert.Empty(t, ticket.args)
	assert.Equal(t, []string{"updated_at = now()", "status = 'queued'", "closed_at = now()", "resume_agent_id = NULL", "affinity_until = NULL"}, attempt.clauses)
	assert.Empty(t, attempt.args)

	ticket, _ = ticketWriteSets(&TicketWrite{Status: To(StatusCancelled), Closing: Closes()})
	assert.Contains(t, ticket.clauses, "closed_at = now()", "a status set directly closes the ticket here")

	approver := uuid.New()
	ticket, attempt = ticketWriteSets(&TicketWrite{
		Status: ToQueue(), FirstClaim: FirstClaimWindowOf(90 * time.Minute), Review: ReviewApproves(approver),
	})
	assert.Equal(t, []string{"updated_at = now()", "blocked_until = NULL", "cooldown_releases = 0", "cooldown_since = NULL"}, ticket.clauses)
	require.Len(t, attempt.clauses, 6)

	until := time.Date(2026, 10, 10, 14, 30, 0, 0, time.UTC)
	since := until.Add(-time.Hour)
	ticket, _ = ticketWriteSets(&TicketWrite{Status: To(StatusBlocked), Cooldown: &CooldownWrite{Until: until, Streak: CooldownStreak{Releases: 2, Since: &since}}})
	assert.Equal(t, []string{"updated_at = now()", "blocked_until = $2", "cooldown_releases = $3", "cooldown_since = $4"}, ticket.clauses)
	assert.Equal(t, []any{until, 2, &since}, ticket.args, "a cooldown release extends the streak instead of ending it")

	for status, direct := range map[Status]bool{
		StatusPreparing: true, StatusParked: true, StatusCancelled: true,
		StatusDone: false, StatusInProgress: false, StatusDiscussion: false, StatusBlocked: false,
	} {
		ticket, _ = ticketWriteSets(&TicketWrite{Status: To(status)})
		assert.Equal(t, direct, slices.Contains(ticket.clauses, "status = $2"),
			"a person's %s is the ticket's own status only outside the queue lifecycle", status)
	}

	ticket, _ = ticketWriteSets(&TicketWrite{NeedsDiscussion: new(bool)})
	assert.NotContains(t, ticket.clauses, "blocked_until = NULL", "a write that moves no status leaves the cooldown")
	assert.Contains(t, attempt.clauses[2], "affinity_until = CASE WHEN EXISTS", "the window is written only for an enabled worker")
	assert.Contains(t, attempt.clauses[2], "now() + $2::interval", "the window opens by the database's clock")
	assert.Contains(t, attempt.clauses[2], "pa.resume_agent_id", "for whoever the attempt says is owed")
	assert.Equal(t, []string{"approved_at = now()", "approved_by_user_id = $3", "changes_requested_at = NULL"}, attempt.clauses[3:],
		"and leaves who it is owed to alone")
	assert.Equal(t, []any{"1h30m0s", approver}, attempt.args)

	ticket, attempt = ticketWriteSets(&TicketWrite{Review: ReviewRequestsChanges()})
	assert.Equal(t, []string{"updated_at = now()"}, ticket.clauses)
	assert.Equal(t, []string{"updated_at = now()", "approved_at = NULL", "approved_by_user_id = NULL", "approved_by_user_id_deleted = false", "changes_requested_at = now()"}, attempt.clauses)
	assert.Empty(t, attempt.args)
}

// A review ends only a ticket in review, and only from its current cycle;
// either refusal writes nothing. Accepted, it requeues with the author's
// window open and the outcome recorded -- with the reviewer's comment, for
// a request for changes.
func TestEndReview(t *testing.T) {
	t.Parallel()
	user := uuid.New()
	affinity := 90 * time.Minute
	started := time.Date(2026, 10, 1, 9, 30, 15, 700_000_000, time.UTC)
	at := func(t time.Time) *time.Time { return &t }

	inReview := &Ticket{ID: 12, Status: StatusReview, ReviewStartedAt: &started}
	got, err := approval(user, affinity, nil)(inReview)
	require.NoError(t, err)
	assert.Equal(t, &Change{Ticket: TicketWrite{
		Status: ToQueue(), FirstClaim: FirstClaimWindowOf(affinity), Review: ReviewApproves(user),
	}}, got)

	got, err = changesRequested(user, "split it", affinity, nil)(inReview)
	require.NoError(t, err)
	assert.Equal(t, &Change{
		Comment: &NewComment{By: UserAuthor(user), Body: "split it"},
		Ticket: TicketWrite{
			Status: ToQueue(), FirstClaim: FirstClaimWindowOf(affinity), Review: ReviewRequestsChanges(),
		},
	}, got)

	for _, status := range []Status{StatusOpen, StatusInProgress, StatusDone, StatusDiscussion} {
		got, err := approval(user, affinity, nil)(&Ticket{ID: 12, Status: status})
		require.ErrorIs(t, err, ErrNotInReview, "from %s", status)
		assert.Contains(t, err.Error(), "#12")
		assert.Nil(t, got)
	}

	cases := []struct {
		name      string
		submitted *time.Time
		started   *time.Time
		stale     bool
	}{
		{"given now", nil, &started, false},
		{"no cycle recorded", at(started.Add(-time.Hour)), nil, false},
		{"after the cycle began", at(started.Add(time.Second)), &started, false},
		// GitHub's second-precision time for a review given in the second
		// the cycle began.
		{"in the second it began", at(started.Truncate(time.Second)), &started, false},
		{"the second before", at(started.Truncate(time.Second).Add(-time.Second)), &started, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := changesRequested(user, "again", affinity, tc.submitted)(
				&Ticket{ID: 12, Status: StatusReview, ReviewStartedAt: tc.started})
			if !tc.stale {
				require.NoError(t, err)
				assert.NotNil(t, got)
				return
			}
			require.ErrorIs(t, err, ErrStaleReview)
			assert.Contains(t, err.Error(), "#12")
			assert.Nil(t, got)
		})
	}
}

// What a person's comment does besides being recorded: it answers a
// discussion, and setting needs_discussion takes a held ticket back.
func TestHumanCommentEffect(t *testing.T) {
	t.Parallel()
	affinity := 90 * time.Minute
	set, cleared := true, false
	answered := TicketWrite{Status: ToQueue(), FirstClaim: FirstClaimWindowOf(affinity)}

	cases := []struct {
		name   string
		status Status
		needs  *bool
		want   *Change
	}{
		{"answers a discussion", StatusDiscussion, nil, &Change{Ticket: answered}},
		{"answers a discussion, flagging it", StatusDiscussion, &set, &Change{Ticket: TicketWrite{
			Status: ToQueue(), FirstClaim: FirstClaimWindowOf(affinity), NeedsDiscussion: &set,
		}}},
		{"answers a discussion, clearing the flag", StatusDiscussion, &cleared, &Change{Ticket: TicketWrite{
			Status: ToQueue(), FirstClaim: FirstClaimWindowOf(affinity), NeedsDiscussion: &cleared,
		}}},
		{"plain comment on a held ticket", StatusInProgress, nil, nil},
		{"plain comment on an open ticket", StatusOpen, nil, nil},
		{"flagging a held ticket takes it back", StatusInProgress, &set, &Change{
			Ticket: TicketWrite{Status: ToQueue(), NeedsDiscussion: &set}, Revoke: true,
		}},
		{"clearing the flag leaves a held ticket held", StatusInProgress, &cleared, &Change{
			Ticket: TicketWrite{NeedsDiscussion: &cleared},
		}},
		{"flagging an open ticket", StatusOpen, &set, &Change{Ticket: TicketWrite{NeedsDiscussion: &set}}},
		{"flagging a ticket in review", StatusReview, &set, &Change{Ticket: TicketWrite{NeedsDiscussion: &set}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := humanCommentEffect(tc.needs, affinity)(&Ticket{ID: 12, Status: tc.status})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A merge on GitHub closes a ticket in review, or approved and waiting --
// as a move to done would -- and nothing an agent holds or is still working
// towards, nor a ticket whose PR is no longer the one merged.
func TestCloseMerged(t *testing.T) {
	t.Parallel()
	user := uuid.New()
	approvedAt := time.Now()
	pr := codehost.ChangeRef{Host: "github.com", Owner: "acme", Repo: "widgets", Number: 12}
	prURL := "https://github.com/Acme/Widgets/pull/12/files"
	otherPR := "https://github.com/acme/widgets/pull/13"
	otherRepo := "https://github.com/acme/gadgets/pull/12"
	otherOwner := "https://github.com/initech/widgets/pull/12"
	closed := &Change{
		Comment: &NewComment{By: UserAuthor(user), Body: "Merged on github.com: https://github.com/acme/widgets/pull/12"},
		Ticket:  TicketWrite{Status: To(StatusDone), Closing: Closes()},
		Unblock: true,
		Revoke:  true,
	}
	cases := []struct {
		name      string
		ticket    Ticket
		mergeable bool
	}{
		{"in review", Ticket{Status: StatusReview, PRURL: &prURL}, true},
		{"approved, open", Ticket{Status: StatusOpen, ApprovedAt: &approvedAt, PRURL: &prURL}, true},
		{"approved, blocked", Ticket{Status: StatusBlocked, ApprovedAt: &approvedAt, PRURL: &prURL}, true},
		{"in the merge queue", Ticket{Status: StatusInMergeQueue, PRURL: &prURL}, true},
		{"approved, held", Ticket{Status: StatusInProgress, ApprovedAt: &approvedAt, PRURL: &prURL}, false},
		{"open, unapproved", Ticket{Status: StatusOpen, PRURL: &prURL}, false},
		{"done", Ticket{Status: StatusDone, ApprovedAt: &approvedAt, PRURL: &prURL}, false},
		{"in review, another PR", Ticket{Status: StatusReview, PRURL: &otherPR}, false},
		{"approved, another PR", Ticket{Status: StatusOpen, ApprovedAt: &approvedAt, PRURL: &otherPR}, false},
		{"in review, same number in another repo", Ticket{Status: StatusReview, PRURL: &otherRepo}, false},
		{"in review, same number for another owner", Ticket{Status: StatusReview, PRURL: &otherOwner}, false},
		{"in review, no PR", Ticket{Status: StatusReview}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.ticket.ID = 12
			got, err := closeMergedFrom(codehosttest.Registry(codehosttest.NewLinkHost("github.com")), pr, user, false)(&tc.ticket)
			if tc.mergeable {
				require.NoError(t, err)
				assert.Equal(t, closed, got)
				return
			}
			require.ErrorIs(t, err, ErrNotMergeable)
			assert.Contains(t, err.Error(), "#12")
			assert.Nil(t, got)
		})
	}
}
