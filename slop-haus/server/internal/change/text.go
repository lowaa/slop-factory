package change

import (
	"fmt"
	"strings"
	"unicode"
)

// People are those a change names: whoever the server knows to have had a
// hand in it. Names are as the board shows them (display names, never email
// addresses).
type People struct {
	Agent             string
	Creator           string
	Approver          string
	ProposalApprovers []string
}

// footer is the part of a change's description, and of its merge commit,
// that the server writes: it is where a reader looks to see who was
// involved, and an agent cannot write over it.
func footer(ticketID int64, ticketURL string, p *People) string {
	var b strings.Builder
	b.WriteString("---\n")
	if ticketURL != "" {
		fmt.Fprintf(&b, "slop-haus #%d: %s\n", ticketID, ticketURL)
	} else {
		fmt.Fprintf(&b, "slop-haus #%d\n", ticketID)
	}
	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s: %s\n", label, value)
		}
	}
	line("Agent", p.Agent)
	line("Ticket filed by", p.Creator)
	line("Approved by", p.Approver)
	line("Proposals approved by", strings.Join(p.ProposalApprovers, ", "))
	return b.String()
}

const maxNameRunes = 80

// personName makes a display name safe to put in text a code host renders:
// one line, and none of the characters that mention a user, open a tag or
// start markup.
func personName(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == ' ', strings.ContainsRune("._-'()&,", r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return truncate(strings.Join(strings.Fields(b.String()), " "), maxNameRunes)
}

const maxSubjectRunes = 200

// singleLine is s with every control character, newlines included, taken
// out, for a commit subject.
func singleLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return truncate(strings.Join(strings.Fields(b.String()), " "), maxSubjectRunes)
}

func truncate(s string, runes int) string {
	r := []rune(s)
	if len(r) <= runes {
		return s
	}
	return string(r[:runes])
}
