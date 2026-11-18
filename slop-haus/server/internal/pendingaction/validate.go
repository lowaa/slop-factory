package pendingaction

import (
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// normalise checks a proposal's payload against its kind and returns what
// is stored. Approve's body is dropped rather than stored: approving takes
// no comment, so a body kept on the row would be shown to the person
// without ever being applied.
func normalise(kind Kind, p Payload) (Payload, error) {
	if !kind.valid() {
		return Payload{}, invalid("unknown kind %q", kind)
	}
	if spec := kind.Spec(); spec.Subject == SubjectTenant {
		if spec.Normalise == nil {
			return Payload{}, invalid("unknown kind %q", kind)
		}
		return spec.Normalise(p)
	}
	if len(p.Body) > MaxTextBytes {
		return Payload{}, invalid("text is %d bytes, over the %d limit", len(p.Body), MaxTextBytes)
	}
	stray := func(field string) error {
		return invalid("%s does not take %s", kind, field)
	}
	if kind != KindEdit && p.Edit != nil {
		return Payload{}, stray("edit")
	}
	if p.ManifestEdit != nil {
		return Payload{}, stray("manifest_edit")
	}
	switch kind {
	case KindEdit:
		return normaliseEdit(p)
	case KindDiscussionAnswer:
		if p.Status != "" {
			return Payload{}, stray("status")
		}
		if p.Decision != "" {
			return Payload{}, stray("decision")
		}
		if blank(p.Body) {
			return Payload{}, invalid("an answer needs text")
		}
		return Payload{Body: p.Body}, nil
	case KindCloseMerged:
		if p.Status != "" || p.Decision != "" {
			return Payload{}, invalid("close_merged takes only a reason")
		}
		if blank(p.Body) {
			return Payload{}, invalid("a reason is required")
		}
		return Payload{Body: p.Body}, nil
	case KindReady:
		if p.Body != "" || p.Status != "" || p.Decision != "" {
			return Payload{}, invalid("ready takes no payload")
		}
		return Payload{}, nil
	case KindStatus:
		if p.Decision != "" {
			return Payload{}, stray("decision")
		}
		if p.Status != ticket.StatusParked && p.Status != ticket.StatusCancelled {
			return Payload{}, invalid("a status proposal moves a ticket to parked or cancelled, not %q", p.Status)
		}
		if blank(p.Body) {
			return Payload{}, invalid("a reason is required")
		}
		return Payload{Status: p.Status, Body: p.Body}, nil
	case KindReviewDecision:
		if p.Status != "" {
			return Payload{}, stray("status")
		}
		switch p.Decision {
		case DecisionApprove:
			return Payload{Decision: DecisionApprove}, nil
		case DecisionRequestChanges:
			if blank(p.Body) {
				return Payload{}, invalid("requesting changes needs text saying what to address")
			}
			return Payload{Decision: DecisionRequestChanges, Body: p.Body}, nil
		}
		return Payload{}, invalid("a review decision is approve or request_changes, not %q", p.Decision)
	}
	return Payload{}, invalid("unknown kind %q", kind)
}

func normaliseEdit(p Payload) (Payload, error) {
	e := p.Edit
	if p.Body != "" || p.Status != "" || p.Decision != "" {
		return Payload{}, invalid("edit takes only the edit's fields")
	}
	if e == nil || (e.Title == nil && e.Body == nil && e.Paths == nil && e.Priority == nil) {
		return Payload{}, invalid("an edit needs at least one of title, body, paths or priority")
	}
	if e.BaseRevision < 0 {
		return Payload{}, invalid("base_revision is a ticket revision, not %d", e.BaseRevision)
	}
	if e.Title != nil && blank(*e.Title) {
		return Payload{}, invalid("a title cannot be blank")
	}
	if e.Body != nil && len(*e.Body) > MaxTextBytes {
		return Payload{}, invalid("body is %d bytes, over the %d limit", len(*e.Body), MaxTextBytes)
	}
	if e.Title != nil && len(*e.Title) > MaxTextBytes {
		return Payload{}, invalid("title is %d bytes, over the %d limit", len(*e.Title), MaxTextBytes)
	}
	if e.Paths != nil && len(e.Paths) == 0 {
		return Payload{}, invalid("a ticket cannot be left with no paths")
	}
	if e.Priority != nil && !e.Priority.Valid() {
		return Payload{}, invalid("unknown priority %q", *e.Priority)
	}
	edit := *e
	return Payload{Edit: &edit}, nil
}

// checkAgainstTicket is the rule a person would meet doing the action now,
// against the ticket as read inside the proposing transaction.
func checkAgainstTicket(kind Kind, p Payload, b *Basis) error {
	need := func(want ticket.Status) error {
		if b.Status != want {
			return invalid("%s needs a ticket in %s, this one is %s", kind, want, b.Status)
		}
		return nil
	}
	switch kind {
	case KindDiscussionAnswer:
		return need(ticket.StatusDiscussion)
	case KindReady:
		return need(ticket.StatusPreparing)
	case KindReviewDecision:
		return need(ticket.StatusReview)
	case KindEdit:
		if b.Status.IsTerminal() {
			return invalid("a ticket that is %s cannot be edited", b.Status)
		}
		if p.Edit.Open && b.Status != ticket.StatusPreparing {
			return invalid("an edit that opens a ticket needs one in %s, this one is %s", ticket.StatusPreparing, b.Status)
		}
		if p.Edit.BaseRevision != b.Revision {
			return invalid("the ticket is at revision %d, not the %d this edit was made against: read it again and re-propose",
				b.Revision, p.Edit.BaseRevision)
		}
	case KindCloseMerged:
		if b.Status.IsTerminal() {
			return invalid("a ticket that is %s cannot be closed as merged", b.Status)
		}
	case KindStatus:
		if b.Status == p.Status || b.Status.IsTerminal() {
			return invalid("a ticket that is %s cannot be moved to %s", b.Status, p.Status)
		}
		if err := ticket.CheckHumanMove(b.Status, p.Status); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	return nil
}
