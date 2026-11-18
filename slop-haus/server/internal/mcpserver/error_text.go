package mcpserver

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// noticeKey marks a call that has just handed the agent its revocation
// notice, so the same call does not follow it with the context line.
type noticeKey struct{}

func noticeDelivered(ctx context.Context) bool {
	delivered, _ := ctx.Value(noticeKey{}).(bool)
	return delivered
}

// packagePrefix is the Go-convention "pkg: " the error values carry. They
// stay in the values for logs; the agent has no use for them, so they are
// dropped where an error becomes tool text. Listed rather than matched
// loosely so that a sentence such as "Note: ..." is never touched.
var packagePrefix = regexp.MustCompile(`(^|\()(ticket|change|codehost|github|githubapp): `)

// handOverClause matches the merge refusal for a head that the approval does
// not cover: its sentinel ends with the instruction, and the wrapper appends
// the particulars after it, which reads as a sentence that starts mid-way.
var handOverClause = regexp.MustCompile(`^(.*): (hand it over again with ticket_finish): (.*)$`)

// agentText is what the agent reads for a failed call. Logs keep the error.
func agentText(err error) string {
	msg := err.Error()
	msg = handOverClause.ReplaceAllString(msg, "$1: $3. Please $2.")
	msg = packagePrefix.ReplaceAllString(msg, "$1")
	return msg
}

// failure turns a tool's error into the text the agent reads. A bare "holds
// no lease" gets the reason when a human ended the agent's last lease and
// the notice is already delivered: that covers a notice that was received
// but lost on its way to the model.
func (h *Handler) failure(ctx context.Context, agentID uuid.UUID, err error) string {
	if errors.Is(err, ticket.ErrNoLease) && err.Error() == ticket.ErrNoLease.Error() {
		if line := h.revocationContext(ctx, agentID); line != "" {
			return line
		}
	}
	return agentText(err)
}

// failureOf is err itself when its text needs no change, so a typed error
// the SDK treats specially keeps its type.
func (h *Handler) failureOf(ctx context.Context, agentID uuid.UUID, err error) error {
	if msg := h.failure(ctx, agentID, err); msg != err.Error() {
		return errors.New(msg)
	}
	return err
}

// revocationContext is the line saying why the agent holds nothing, or "".
func (h *Handler) revocationContext(ctx context.Context, agentID uuid.UUID) string {
	if noticeDelivered(ctx) {
		return ""
	}
	found, err := authenticatedAgent(ctx)
	if err != nil || found.ReadOnly() {
		return ""
	}
	recent, err := h.tickets.RecentRevocation(ctx, agentID)
	if err != nil {
		h.logger.Warn("load recent revocation", "agent_id", agentID, "err", err)
		return ""
	}
	if recent == nil {
		return ""
	}
	return strings.TrimSpace(recent.Describe())
}
