package steward

import (
	"context"
	"fmt"
)

// RenderAnswer renders the only texts a discussion answer may carry, one per
// category. Approving the answer posts its body verbatim as the owner, so the
// server renders the text from facts it holds and the body is accepted only if
// it equals the rendering: the steward chooses a category, never the words.
func RenderAnswer(category string, s *Subject) (string, bool) {
	switch category {
	case "merge_continue":
		return fmt.Sprintf("Carry on and merge: CI is green on %s and the change is within the ticket's scope.", s.HeadSHA), s.HeadSHA != ""
	case "infra_wait":
		return fmt.Sprintf("An outside system is delayed and nothing on ticket #%d has changed. Wait and check again.", s.TicketID), true
	case "gate_confirmed":
		return fmt.Sprintf("Confirmed: the gate is satisfied on %s. Carry on.", s.HeadSHA), s.HeadSHA != ""
	}
	return "", false
}

// AnswerFromTemplate holds when a discussion answer's body is exactly its
// category's rendering. No other check reads the body, so without it the text
// posted as the owner would be whatever the steward wrote.
func AnswerFromTemplate() Check {
	return checkFunc{CheckAnswerFromTemplate, func(_ context.Context, s *Subject) (Evidence, error) {
		want, ok := RenderAnswer(s.Category, s)
		if !ok {
			return Evidence{Detail: "no answer template applies to " + s.Category}, nil
		}
		if s.Body != want {
			return Evidence{Detail: fmt.Sprintf("the answer is not the %s template; it must read exactly: %q", s.Category, want)}, nil
		}
		return Evidence{OK: true}, nil
	}}
}
