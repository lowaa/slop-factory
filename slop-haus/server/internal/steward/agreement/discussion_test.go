package agreement_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func templateFor(t *testing.T, category string, ticketID int64, head string) string {
	t.Helper()
	body, ok := steward.RenderAnswer(category, &steward.Subject{TicketID: ticketID, HeadSHA: head})
	require.True(t, ok)
	return body
}

func TestCompareLabelAndAnswer(t *testing.T) {
	tmpl := templateFor(t, "merge_continue", 7, "abc")
	for _, c := range []struct {
		name          string
		r             agreement.DiscussionRound
		label, answer agreement.Outcome
	}{
		{"routine approved", agreement.DiscussionRound{Label: "routine", Action: "approve", OriginalBody: tmpl, FinalBody: tmpl}, agreement.Agreed, agreement.Agreed},
		{"routine edited is an answer disagreement only", agreement.DiscussionRound{Label: "routine", Action: "edit", OriginalBody: tmpl, FinalBody: tmpl + " now"}, agreement.Undecided, agreement.Disagreed},
		{"routine edited to the same text agrees", agreement.DiscussionRound{Label: "routine", Action: "edit", OriginalBody: tmpl, FinalBody: tmpl}, agreement.Undecided, agreement.Agreed},
		{"routine rejected is an answer disagreement only", agreement.DiscussionRound{Label: "routine", Action: "reject", OriginalBody: tmpl}, agreement.Undecided, agreement.Disagreed},
		{"routine answered elsewhere disagrees with the label", agreement.DiscussionRound{Label: "routine", OtherAnswer: "do it differently"}, agreement.Disagreed, agreement.Undecided},
		{"routine unanswered", agreement.DiscussionRound{Label: "routine"}, agreement.Undecided, agreement.Undecided},
		{"yours free text agrees", agreement.DiscussionRound{Label: "yours", Action: "edit", FinalBody: "use option B"}, agreement.Agreed, agreement.Undecided},
		{"yours free text elsewhere agrees", agreement.DiscussionRound{Label: "yours", OtherAnswer: "use option B"}, agreement.Agreed, agreement.Undecided},
		{"yours answered with a template disagrees", agreement.DiscussionRound{Label: "yours", Action: "edit", FinalBody: tmpl}, agreement.Disagreed, agreement.Undecided},
		{"yours template elsewhere disagrees", agreement.DiscussionRound{Label: "yours", OtherAnswer: tmpl}, agreement.Disagreed, agreement.Undecided},
		{"yours dismissed is unscored", agreement.DiscussionRound{Label: "yours", Action: "reject"}, agreement.Undecided, agreement.Undecided},
		{"not assessed is unscored", agreement.DiscussionRound{Label: "not_assessed", OtherAnswer: "x"}, agreement.Undecided, agreement.Undecided},
		{"pending is unscored", agreement.DiscussionRound{Label: "pending", Action: "approve"}, agreement.Undecided, agreement.Undecided},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.r
			r.TicketID, r.HeadSHA = 7, "abc"
			assert.Equal(t, c.label, agreement.CompareLabel(&r), "label")
			assert.Equal(t, c.answer, agreement.CompareAnswer(&r), "answer")
		})
	}
}

func TestOverriddenRoutineIsOnlyRoutineAnsweredElsewhere(t *testing.T) {
	assert.True(t, agreement.OverriddenRoutine(&agreement.DiscussionRound{Label: "routine", OtherAnswer: "x"}))
	assert.False(t, agreement.OverriddenRoutine(&agreement.DiscussionRound{Label: "yours", OtherAnswer: "x"}))
	assert.False(t, agreement.OverriddenRoutine(&agreement.DiscussionRound{Label: "routine", Action: "edit", OtherAnswer: "x"}))
}

func (r *rig) discussionTicket(t *testing.T) int64 {
	t.Helper()
	created, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "discussed", Status: ticket.StatusOpen})
	require.NoError(t, err)
	return created.ID
}

func (r *rig) label(t *testing.T, ticketID int64, round int, label, category string, at time.Time) string {
	t.Helper()
	var assessedAt any = at
	if label == "not_assessed" {
		assessedAt = nil
	}
	var id string
	require.NoError(t, r.database.QueryRow(r.ctx, `
		INSERT INTO steward_discussion_labels (id, ticket_id, attempt, round, label, category, head_sha, assessed_at, created_at)
		VALUES (gen_random_uuid(), $1, 1, $2, $3, $4, 'abc', $5, $6) RETURNING id::text`,
		ticketID, round, label, category, assessedAt, at).Scan(&id))
	return id
}

func (r *rig) decide(t *testing.T, labelID, action, original, final, reason string, at time.Time) {
	t.Helper()
	_, err := r.database.Exec(r.ctx, `
		INSERT INTO steward_discussion_decisions (id, label_id, action, original_body, final_body, reason, actor, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, 'owner', $6)`, labelID, action, original, final, reason, at)
	require.NoError(t, err)
}

func (r *rig) ownerComment(t *testing.T, ticketID int64, body string, at time.Time) {
	t.Helper()
	_, err := r.database.Exec(r.ctx, `INSERT INTO ticket_comments (ticket_id, author_user_id, body, created_at) VALUES ($1, $2, $3, $4)`,
		ticketID, r.approver.ID, body, at)
	require.NoError(t, err)
}

func (r *rig) agentComment(t *testing.T, ticketID int64, onBehalf bool, attempt *int, body string, at time.Time) {
	t.Helper()
	if attempt != nil {
		_, err := r.database.Exec(r.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, $2, 'queued') ON CONFLICT DO NOTHING`, ticketID, *attempt)
		require.NoError(t, err)
	}
	var agentID, owner any = r.console, nil
	if onBehalf {
		owner = r.approver.ID
	}
	_, err := r.database.Exec(r.ctx, `INSERT INTO ticket_comments (ticket_id, author_agent_id, on_behalf_of_user_id, attempt_number, body, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, ticketID, agentID, owner, attempt, body, at)
	require.NoError(t, err)
}

func TestScoreDiscussionsReadsWhoAnsweredAndWhichAttempt(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	at := func(m int) time.Time { return epoch.Add(time.Duration(m) * time.Minute) }
	one, other := 1, 2

	// An agent's own comment is not the owner's answer.
	agentOnly := r.discussionTicket(t)
	r.label(t, agentOnly, 1, "routine", "merge_continue", at(0))
	r.agentComment(t, agentOnly, false, nil, "status note", at(1))

	// Remote Control speaks for the owner: that is the owner going elsewhere.
	remote := r.discussionTicket(t)
	r.label(t, remote, 1, "routine", "merge_continue", at(2))
	r.agentComment(t, remote, true, &one, "answered via remote control", at(3))

	// A comment addressed to another attempt is not this attempt's answer.
	sibling := r.discussionTicket(t)
	r.label(t, sibling, 1, "routine", "merge_continue", at(4))
	r.agentComment(t, sibling, true, &other, "for attempt two", at(5))

	// A decision wins over the comment it posted itself.
	decided := r.discussionTicket(t)
	tmpl := templateFor(t, "merge_continue", decided, "abc")
	r.decide(t, r.label(t, decided, 1, "routine", "merge_continue", at(6)), "approve", tmpl, tmpl, "", at(7))
	r.ownerComment(t, decided, tmpl, at(7))

	// An owner comment after the next round began belongs to that round.
	later := r.discussionTicket(t)
	r.label(t, later, 1, "routine", "merge_continue", at(8))
	r.label(t, later, 2, "yours", "", at(9))
	r.ownerComment(t, later, "free text", at(10))

	rep, err := r.scorer.ScoreDiscussions(r.ctx, agreement.Window{})
	require.NoError(t, err)
	byCat := map[string]agreement.DiscussionLabelScore{}
	for _, l := range rep.Labels {
		byCat[l.Category] = l
	}
	routine := byCat["merge_continue"]
	assert.Equal(t, 2, routine.Decisions, "remote control and the approval; nothing else")
	assert.Equal(t, 1, routine.Agreed)
	require.Len(t, routine.Latest, 1)
	assert.Equal(t, remote, routine.Latest[0].TicketID)
	assert.Equal(t, 1, byCat["yours"].Decisions)
	assert.Equal(t, 1, byCat["yours"].Agreed)
}

func TestScoreDiscussionsKeepsLabelsAndAnswersApartAndListsOverriddenFirst(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	tmpl := func(id int64) string { return templateFor(t, "merge_continue", id, "abc") }
	at := func(m int) time.Time { return epoch.Add(time.Duration(m) * time.Minute) }

	approved := r.discussionTicket(t)
	r.decide(t, r.label(t, approved, 1, "routine", "merge_continue", at(0)), "approve", tmpl(approved), tmpl(approved), "", at(1))

	edited := r.discussionTicket(t)
	r.decide(t, r.label(t, edited, 1, "routine", "merge_continue", at(2)), "edit", tmpl(edited), "go ahead, differently", "", at(3))

	// An older template-equal yours answer, then a newer routine override: the
	// override still lists first.
	yoursTemplate := r.discussionTicket(t)
	r.label(t, yoursTemplate, 1, "yours", "", at(4))
	r.ownerComment(t, yoursTemplate, tmpl(yoursTemplate), at(5))

	overridden := r.discussionTicket(t)
	r.label(t, overridden, 1, "routine", "merge_continue", at(6))
	r.ownerComment(t, overridden, "no, hold off", at(7))

	yoursFree := r.discussionTicket(t)
	r.label(t, yoursFree, 1, "yours", "", at(8))
	r.ownerComment(t, yoursFree, "pick option B", at(9))

	r.label(t, r.discussionTicket(t), 1, "not_assessed", "", at(10))

	// A second round's answer does not leak into the first round.
	rounds := r.discussionTicket(t)
	r.label(t, rounds, 1, "yours", "", at(11))
	r.ownerComment(t, rounds, "first answer, free text", at(12))
	r.label(t, rounds, 2, "routine", "merge_continue", at(13))

	rep, err := r.scorer.ScoreDiscussions(r.ctx, agreement.Window{})
	require.NoError(t, err)

	require.Len(t, rep.Labels, 2)
	routine, yours := rep.Labels[0], rep.Labels[1]
	assert.Equal(t, "merge_continue", routine.Category)
	assert.Equal(t, 2, routine.Decisions)
	assert.Equal(t, 1, routine.Agreed)
	require.Len(t, routine.Latest, 1)
	assert.Equal(t, overridden, routine.Latest[0].TicketID)
	assert.True(t, routine.Latest[0].Overridden)

	assert.Equal(t, "yours", yours.Category)
	assert.Equal(t, 3, yours.Decisions)
	assert.Equal(t, 2, yours.Agreed)
	require.Len(t, yours.Latest, 1)
	assert.Equal(t, yoursTemplate, yours.Latest[0].TicketID)

	require.Len(t, rep.Answers, 1)
	ans := rep.Answers[0]
	assert.Equal(t, "merge_continue", ans.Category)
	assert.Equal(t, 2, ans.Decisions)
	assert.Equal(t, 1, ans.Agreed)
	require.Len(t, ans.Latest, 1)
	assert.Equal(t, edited, ans.Latest[0].TicketID)
	assert.Equal(t, "go ahead, differently", ans.Latest[0].FinalBody)
	assert.Equal(t, tmpl(edited), ans.Latest[0].OriginalBody)
}

func TestScoreDiscussionsLeadsWithOverriddenAndHonoursTheWindow(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	at := func(m int) time.Time { return epoch.Add(time.Duration(m) * time.Minute) }
	var overridden int64
	for i := 0; i < 2; i++ {
		id := r.discussionTicket(t)
		r.label(t, id, 1, "routine", "merge_continue", at(i*10))
		r.ownerComment(t, id, fmt.Sprintf("other route %d", i), at(i*10+1))
		overridden = id
	}
	rep, err := r.scorer.ScoreDiscussions(r.ctx, agreement.Window{Since: at(5)})
	require.NoError(t, err)
	require.Len(t, rep.Labels, 1)
	require.Len(t, rep.Labels[0].Latest, 1)
	assert.Equal(t, overridden, rep.Labels[0].Latest[0].TicketID)

	_, err = r.scorer.WithScanLimit(1).ScoreDiscussions(r.ctx, agreement.Window{})
	assert.ErrorIs(t, err, agreement.ErrWindowTooLarge)
}
