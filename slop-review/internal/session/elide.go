package session

import (
	"fmt"

	"github.com/lowaa/slop-factory/slop-review/internal/llm"
)

// elidedArgsMax bounds how much of a call's arguments the elision stub
// repeats: enough to recognise the call and make it again, no more.
const elidedArgsMax = 200

// elide keeps the conversation within the context budget by replacing the
// oldest tool results with a stub naming the call that produced them.
//
// Every turn resends the whole conversation, so without this each file a
// reviewer reads is paid for on every later turn, and a long session on a
// big change runs out of context window before it runs out of turns. Old
// tool results are the right thing to drop: they are the bulk of the
// transcript, the model's own reasoning about them stays in its assistant
// turns, findings live outside the transcript entirely (see recorded), and
// anything still needed can simply be fetched again. The system prompt,
// the review input, and the latest turn's results are never elided.
func (s *SlopReviewManagedSession) elide() {
	if s.contextBudget <= 0 {
		return
	}
	size := 0
	for _, m := range s.messages {
		size += messageSize(m)
	}
	if size <= s.contextBudget {
		return
	}

	protectedFrom := len(s.messages)
	for i := len(s.messages) - 1; i >= 0; i-- {
		if s.messages[i].Role == llm.RoleAssistant {
			protectedFrom = i
			break
		}
	}

	calls := map[string]llm.ToolCall{}
	for _, m := range s.messages {
		for _, tc := range m.ToolCalls {
			calls[tc.ID] = tc
		}
	}

	for i := 2; i < protectedFrom && size > s.contextBudget; i++ {
		m := &s.messages[i]
		if m.Role != llm.RoleTool {
			continue
		}
		stub := elisionStub(calls[m.ToolCallID])
		if len(stub) >= len(m.Content) {
			continue
		}
		size -= len(m.Content) - len(stub)
		m.Content = stub
		s.elisions++
	}
}

func elisionStub(tc llm.ToolCall) string {
	args := tc.ArgumentsJSON
	if len(args) > elidedArgsMax {
		args = args[:elidedArgsMax] + "..."
	}
	return fmt.Sprintf("[result elided to stay within the context budget: %s %s -- call it again if you still need it]", tc.Name, args)
}

// messageSize deliberately leaves out ProviderState: it is opaque (often
// encrypted) bytes whose length says nothing about the context the model
// spends on it, and elision could never shrink it anyway -- assistant
// messages are never elided -- so counting it would only elide tool
// results for no gain.
func messageSize(m llm.Message) int {
	size := len(m.Content)
	for _, tc := range m.ToolCalls {
		size += len(tc.Name) + len(tc.ArgumentsJSON)
	}
	return size
}
