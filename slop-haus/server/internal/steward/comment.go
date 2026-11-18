package steward

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Commenter posts a ticket comment authored as the steward agent that made the
// decision. The steward is a tenant-wide helper: its comment is how the ticket
// shows what was done to it and by whom.
type Commenter interface {
	CommentAsSteward(ctx context.Context, stewardID uuid.UUID, ticketID int64, body string) error
}

// LinkKind is what a link in a steward comment lets the owner do.
type LinkKind string

const (
	LinkVeto LinkKind = "veto"
	LinkUndo LinkKind = "undo"
)

// Links builds the board link for the owner to veto or undo a decision.
type Links func(ctx context.Context, kind LinkKind, rec *Record) string

// maxCommentReason bounds the steward's own words in a comment: they are
// untrusted and a ticket's thread is read by agents.
const maxCommentReason = 500

// pendingComment is what the steward posts when it opens a veto window.
func pendingComment(rec *Record, deadline time.Time, link string) string {
	var b strings.Builder
	writeHeadline(&b, rec)
	fmt.Fprintf(&b, "\n\nWill be applied at %s unless vetoed. The proposal stays with you either way.", deadline.UTC().Format(time.RFC3339))
	writeEvidence(&b, rec)
	if link != "" {
		fmt.Fprintf(&b, "\n\nVeto (pending): %s", link)
	}
	return b.String()
}

// appliedComment is what the steward posts after applying a decision.
func appliedComment(rec *Record, link string) string {
	var b strings.Builder
	writeHeadline(&b, rec)
	fmt.Fprintf(&b, "\n\nApplied as the %s tier.", rec.Tier)
	writeEvidence(&b, rec)
	if link != "" {
		fmt.Fprintf(&b, "\n\nUndo (applied): %s", link)
	}
	return b.String()
}

func vetoedComment(rec *Record) string {
	var b strings.Builder
	writeHeadline(&b, rec)
	b.WriteString("\n\nVetoed by the owner before it was applied. The proposal stays pending for them.")
	return b.String()
}

func writeHeadline(b *strings.Builder, rec *Record) {
	reason := strings.Join(strings.Fields(rec.Reason), " ")
	if len(reason) > maxCommentReason {
		reason = reason[:maxCommentReason] + "..."
	}
	fmt.Fprintf(b, "[steward] %s: %s", rec.Category, reason)
}

func writeEvidence(b *strings.Builder, rec *Record) {
	if rec.HeadSHA != nil {
		fmt.Fprintf(b, "\n\nHead: %s", *rec.HeadSHA)
	}
	if len(rec.Evidence) == 0 {
		return
	}
	b.WriteString("\n\nEvidence:")
	for _, ev := range rec.Evidence {
		mark := "ok"
		if !ev.OK {
			mark = "failed"
		}
		fmt.Fprintf(b, "\n- %s (%s)", ev.Check, mark)
		if ev.Ref != "" {
			fmt.Fprintf(b, " %s", ev.Ref)
		}
		if ev.Detail != "" {
			fmt.Fprintf(b, ": %s", strings.Join(strings.Fields(ev.Detail), " "))
		}
	}
}

// postComment posts body on the record's ticket. It never fails the decision
// it describes, which is already recorded: a missing comment is logged.
func postComment(ctx context.Context, c Commenter, logger *slog.Logger, rec *Record, body string) {
	if c == nil || rec.TicketID == nil {
		return
	}
	if err := c.CommentAsSteward(ctx, rec.AgentID, *rec.TicketID, body); err != nil {
		logger.WarnContext(ctx, "steward could not comment on its decision", "assessment", rec.ID, "ticket", *rec.TicketID, "error", err)
	}
}
