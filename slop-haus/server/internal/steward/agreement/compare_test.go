package agreement_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
)

func TestEveryCategoryHasAComparator(t *testing.T) {
	for _, c := range steward.Categories() {
		assert.True(t, agreement.Registered(c.ID), "category %s needs a comparator", c.ID)
	}
}

func TestSameActionComparison(t *testing.T) {
	now := time.Now()
	dec := func(d steward.Decision) *steward.Decision { return &d }
	for _, c := range []struct {
		name string
		rec  steward.Record
		want agreement.Outcome
	}{
		{"approved agrees", steward.Record{Verdict: steward.VerdictWouldAuto, HumanDecision: dec(steward.DecisionApproved)}, agreement.Agreed},
		{"rejected disagrees", steward.Record{Verdict: steward.VerdictWouldVeto, HumanDecision: dec(steward.DecisionRejected)}, agreement.Disagreed},
		{"edited disagrees", steward.Record{Verdict: steward.VerdictWouldVeto, HumanDecision: dec(steward.DecisionEdited)}, agreement.Disagreed},
		{"withdrawn is no decision", steward.Record{Verdict: steward.VerdictWouldAuto, HumanDecision: dec(steward.DecisionWithdrawn)}, agreement.Undecided},
		{"undecided", steward.Record{Verdict: steward.VerdictWouldAuto, CreatedAt: now}, agreement.Undecided},
		{"needs_human claims nothing", steward.Record{Verdict: steward.VerdictNeedsHuman, HumanDecision: dec(steward.DecisionApproved)}, agreement.Undecided},
		{"veto disagrees", steward.Record{Verdict: steward.VerdictWouldVeto, VetoedAt: &now}, agreement.Disagreed},
		{"undo disagrees", steward.Record{Verdict: steward.VerdictWouldAuto, UndoneAt: &now}, agreement.Disagreed},
		{"undo beats an approval", steward.Record{Verdict: steward.VerdictWouldAuto, HumanDecision: dec(steward.DecisionApproved), UndoneAt: &now}, agreement.Disagreed},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, cat := range steward.Categories() {
				assert.Equal(t, c.want, agreement.ComparatorFor(cat.ID)(&c.rec), cat.ID)
			}
			for _, retired := range []string{"restart_resume", "open_ticket"} {
				assert.Equal(t, c.want, agreement.ComparatorFor(retired)(&c.rec), "retired category "+retired)
			}
		})
	}
}

func TestReviewApproveDisagreementIsDangerous(t *testing.T) {
	assert.True(t, agreement.Dangerous("review_approve"))
	assert.False(t, agreement.Dangerous("priority"))
}

func TestTicketReadyExpiresAfterAWeekButRetiredOpenTicketDoesNot(t *testing.T) {
	at := func(age time.Duration) *steward.Record {
		return &steward.Record{Verdict: steward.VerdictWouldVeto, CreatedAt: time.Now().Add(-age)}
	}
	ready := agreement.ComparatorFor("ticket_ready")
	assert.Equal(t, agreement.Undecided, ready(at(6*24*time.Hour)))
	assert.Equal(t, agreement.Disagreed, ready(at(8*24*time.Hour)))
	assert.Equal(t, agreement.Undecided, agreement.ComparatorFor("open_ticket")(at(365*24*time.Hour)))
}

func TestForDigestUsesTheCategoryComparator(t *testing.T) {
	old := steward.Record{Category: "ticket_ready", Verdict: steward.VerdictWouldVeto, CreatedAt: time.Now().Add(-30 * 24 * time.Hour)}
	agreed, disagreed := agreement.ForDigest(&old)
	assert.False(t, agreed)
	assert.True(t, disagreed, "left unopened past ticket_ready's window")

	old.Category = "priority"
	agreed, disagreed = agreement.ForDigest(&old)
	assert.False(t, agreed || disagreed, "a plain category waits on the owner")

	approved := steward.DecisionApproved
	agreed, disagreed = agreement.ForDigest(&steward.Record{Category: "priority", Verdict: steward.VerdictWouldAuto, HumanDecision: &approved})
	assert.True(t, agreed)
	assert.False(t, disagreed)
}
