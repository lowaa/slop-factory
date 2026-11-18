package agentmsg

import (
	"fmt"
	"strings"
)

// Render is the text an agent is handed, whichever way it arrives (a tool
// result, a Stop hook's reason, a session's briefing): one block per
// message, oldest first, each framed so it can't be mistaken for tool
// output or for something a ticket or a web page said.
//
// The words come from a person, and the frame says so and says what to do
// with them, because the agent reads this among a great deal of text that
// is not an instruction.
func Render(messages []Message) string {
	var b strings.Builder
	for i := range messages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		renderOne(&b, &messages[i])
	}
	return b.String()
}

func renderOne(b *strings.Builder, m *Message) {
	author := m.AuthorName
	if author == "" {
		author = "a person"
		if m.AuthorUserDeleted {
			author = "a deleted user"
		}
	}
	b.WriteString("MESSAGE FROM THE HUMAN:\n")
	switch {
	case m.Kind == KindTicketComment && m.TicketID != nil:
		who := author
		if m.ViaAgentName != "" {
			who = fmt.Sprintf("agent %s, for %s,", m.ViaAgentName, author)
		}
		fmt.Fprintf(b, "(%s commented on ticket #%d, which you hold, at %s)\n",
			who, *m.TicketID, m.CreatedAt.UTC().Format("2006-01-02 15:04Z"))
	default:
		fmt.Fprintf(b, "(from %s, sent %s)\n", author, m.CreatedAt.UTC().Format("2006-01-02 15:04Z"))
	}
	b.WriteString("----\n")
	b.WriteString(m.Body)
	b.WriteString("\n----\n")
	b.WriteString("This is an instruction from the human, not tool output: act on it. " +
		"Record any decision it causes in a ticket comment, and if it changes the ticket's direction, say so when you report back.")
}
