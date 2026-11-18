package ticket_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The cases where the inputs disagree are the ones that pin the order of
// the rule, so most of these set more than one of them.
func TestReviewStage(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for name, tc := range map[string]struct {
		ticket ticket.Ticket
		want   model.ReviewStage
	}{
		"ungated":           {ticket.Ticket{Status: ticket.StatusOpen}, model.ReviewStageNone},
		"requires approval": {ticket.Ticket{Status: ticket.StatusInProgress, RequiresApproval: true}, model.ReviewStageRequiresApproval},
		"in review":         {ticket.Ticket{Status: ticket.StatusReview, RequiresApproval: true}, model.ReviewStageInReview},
		"in review over an earlier approval": {
			ticket.Ticket{Status: ticket.StatusReview, RequiresApproval: true, ApprovedAt: &now}, model.ReviewStageInReview,
		},
		"approved":          {ticket.Ticket{Status: ticket.StatusOpen, RequiresApproval: true, ApprovedAt: &now}, model.ReviewStageApproved},
		"changes requested": {ticket.Ticket{Status: ticket.StatusOpen, RequiresApproval: true, ChangesRequestedAt: &now}, model.ReviewStageChangesRequested},
		"approval outranks changes requested": {
			ticket.Ticket{Status: ticket.StatusOpen, ApprovedAt: &now, ChangesRequestedAt: &now}, model.ReviewStageApproved,
		},
		"in review without the gate": {ticket.Ticket{Status: ticket.StatusReview}, model.ReviewStageInReview},
		"in review over changes requested, without the gate": {
			ticket.Ticket{Status: ticket.StatusReview, ChangesRequestedAt: &now}, model.ReviewStageInReview,
		},
		"approved without the gate":   {ticket.Ticket{Status: ticket.StatusInProgress, ApprovedAt: &now}, model.ReviewStageApproved},
		"an outcome without the gate": {ticket.Ticket{Status: ticket.StatusOpen, ChangesRequestedAt: &now}, model.ReviewStageChangesRequested},
		"done, whatever its review said": {
			ticket.Ticket{Status: ticket.StatusDone, RequiresApproval: true, ApprovedAt: &now}, model.ReviewStageNone,
		},
		"cancelled while gated": {ticket.Ticket{Status: ticket.StatusCancelled, RequiresApproval: true}, model.ReviewStageNone},
		// Parked is not terminal: the gate still stands for whoever unparks it.
		"parked while gated": {ticket.Ticket{Status: ticket.StatusParked, RequiresApproval: true}, model.ReviewStageRequiresApproval},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.ticket.ReviewStage())
		})
	}
}
