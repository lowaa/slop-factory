package agreement

import "github.com/lowaa/slop-factory/slop-haus/server/internal/steward"

// Label values a discussion round carries. They mirror discussionlabel's, which
// this package cannot import without a cycle through the steward packages.
const (
	LabelRoutine = "routine"
	LabelYours   = "yours"
)

// Action values of the owner's decision on a draft.
const (
	ActionApprove = "approve"
	ActionEdit    = "edit"
	ActionReject  = "reject"
)

// DiscussionRound is one labelled discussion round with what the owner did
// about it. The label and the answer are scored apart, from the same round.
type DiscussionRound struct {
	TicketID int64
	Attempt  int
	Round    int
	// Label is routine, yours, not_assessed or pending.
	Label    string
	Category string
	HeadSHA  string
	// Action is the owner's decision on the draft, or empty when they took
	// none through "Answer with steward".
	Action       string
	OriginalBody string
	FinalBody    string
	// OtherAnswer is what the owner posted by another route (Remote Control or
	// a plain comment) when no decision exists; empty when nothing was.
	OtherAnswer string
}

// CompareLabel scores whether the classification was right.
//
// A rejected or edited draft says nothing about the label: the owner may have
// disliked only the wording, so it counts for the answer alone. A not-assessed
// or pending label made no claim and is unscored.
func CompareLabel(r *DiscussionRound) Outcome {
	switch r.Label {
	case LabelRoutine:
		switch r.Action {
		case ActionApprove:
			return Agreed
		case "":
			if r.OtherAnswer != "" {
				return Disagreed
			}
		}
	case LabelYours:
		body := r.FinalBody
		if r.Action == "" {
			body = r.OtherAnswer
		}
		if body == "" || r.Action == ActionReject {
			return Undecided
		}
		if equalsATemplate(body, r) {
			return Disagreed
		}
		return Agreed
	}
	return Undecided
}

// OverriddenRoutine reports the label disagreement the steward is told never to
// risk: it said routine and the owner answered by another route.
func OverriddenRoutine(r *DiscussionRound) bool {
	return r.Label == LabelRoutine && r.Action == "" && r.OtherAnswer != ""
}

// CompareAnswer scores the drafted answer. Any text change is a disagreement,
// whether or not the edit stays within a template: the templates have fixed
// wording, so counting small edits as agreement would show agreement for drafts
// the owner did not accept. Only a routine label has a draft.
func CompareAnswer(r *DiscussionRound) Outcome {
	if r.Label != LabelRoutine {
		return Undecided
	}
	switch r.Action {
	case ActionApprove:
		return Agreed
	case ActionEdit:
		if r.FinalBody == r.OriginalBody {
			return Agreed
		}
		return Disagreed
	case ActionReject:
		return Disagreed
	}
	return Undecided
}

// equalsATemplate holds when body is exactly what some answer category would
// have rendered for the round: a yours label names no category, so any counts.
func equalsATemplate(body string, r *DiscussionRound) bool {
	subject := &steward.Subject{TicketID: r.TicketID, HeadSHA: r.HeadSHA}
	for _, c := range steward.Categories() {
		if want, ok := steward.RenderAnswer(c.ID, subject); ok && want == body {
			return true
		}
	}
	return false
}
