// Package tuidriver is the shared kit behind every TUI harness driver
// (agy/tui, and any later */tui): it runs the harness's own TUI in a
// SessionHost (tmux) and maps it, through a per-harness Profile, onto
// harness.Conversation.
//
// What it knows comes from the harness's hooks first, through a HookHub;
// from screen markers, polled, where a hook is missing (agy fires none on
// cancel); and from pane death for the exit. It types into the TUI only
// when it must: a message goes in through a hook reply where the profile
// can inject one, and otherwise as a short pointer, pasted while the
// harness is idle and never while a person is attached.
package tuidriver

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// Driver is the driver name every TUI implementation registers under.
const Driver = "tui"

// Profile is everything the kit needs to know about one harness's TUI.
type Profile struct {
	// Harness is the harness's name, e.g. "agy".
	Harness string
	// Command is the TUI's launch: a new conversation, or resume's when it
	// is non-nil. Env is the complete environment.
	Command func(ws *harness.Workspace, resume *harness.ConversationRef) (*harness.LaunchSpec, error)
	// InstallHooks makes the session run hook (argv, the event name
	// appended) for each hook event, session-only: through spec's flags or
	// environment, or files in the workspace, never the user's own config.
	InstallHooks func(ws *harness.Workspace, spec *harness.LaunchSpec, hook []string) error
	// SubmitKey and CancelKey are tmux key names, e.g. "Enter", "Escape".
	SubmitKey string
	CancelKey string
	Markers   *Markers
	// Quit is what ends the session cleanly when the harness is idle.
	Quit []Input
	// ParseHook reads one hook invocation's payload.
	ParseHook func(event string, payload []byte) (*HookCall, error)
	// Reply is the JSON the harness reads back from a hook, carrying the
	// messages to inject, if any. It is called for every hook, because some
	// harnesses read a missing answer as a refusal (agy's PreToolUse).
	Reply func(call *HookCall, inject []*harness.Message) ([]byte, error)
	// Injects means some hook replies can carry messages to the model (see
	// HookCall.Inject).
	Injects bool
	// Rendered says whether a paste of text shows on screen, given the
	// screen before it and now. nil is the default: text appears more often
	// than it did before (the same pointer may still be on screen from an
	// earlier delivery), ignoring whitespace, which line wrapping inserts.
	Rendered func(before, after, text string) bool
}

// Markers recognise the harness's state on screen. They are the fallback
// for what its hooks don't say.
type Markers struct {
	// Busy and Idle match while a turn runs, and at an idle prompt.
	Busy *regexp.Regexp
	Idle *regexp.Regexp
	// Interrupted, optional, matches once a turn was cancelled.
	Interrupted *regexp.Regexp
}

// Input is one step of a key sequence: text typed literally, then keys.
type Input struct {
	Text string
	Keys []string
}

// InjectPoint is where a hook's reply can put a message in front of the
// model.
type InjectPoint int

const (
	// InjectNone: the reply can't carry a message.
	InjectNone InjectPoint = iota
	// InjectMidTurn: the model reads it before its next step (agy's
	// PreInvocation injectSteps).
	InjectMidTurn
	// InjectTurnEnd: the turn is ending, and a reply with a message keeps it
	// going (agy's Stop continue). The kit then drops the hook's TurnEnded.
	InjectTurnEnd
)

// HookCall is one hook invocation, as the profile reads it.
type HookCall struct {
	// Event is the hook's event name.
	Event string
	// ConversationID is the harness's id, where the payload has one.
	ConversationID string
	// Events are what the hook says happened. Ready means the session can
	// take a message; TurnStarted and TurnEnded bracket a turn. Until Ready,
	// only Ready counts: work before it is the harness's own start-up (agy
	// names a new conversation only once it has run a prompt, so its profile
	// launches with one), and no part of the conversation the core drives.
	Events []harness.Event
	// Submitted means the harness took a prompt: the confirmation a pasted
	// pointer waits for.
	Submitted bool
	Inject    InjectPoint
}

// HookHub routes the hooks a harness runs (`slop-bot hook <event>`) to the
// conversation whose session ran them. The daemon implements it.
type HookHub interface {
	// Route sends session's hook invocations to h until unroute is called.
	Route(session string, h HookHandler) (unroute func(), err error)
	// Command is the argv the session's harness runs for a hook, before the
	// event name.
	Command(session string) []string
}

// HookHandler answers one hook invocation: its event name and the JSON
// payload the harness passed, and the JSON reply the harness reads back.
type HookHandler func(ctx context.Context, event string, payload []byte) ([]byte, error)

func (p *Profile) rendered(before, after, text string) bool {
	if p.Rendered != nil {
		return p.Rendered(before, after, text)
	}
	t := stripSpace(text)
	return strings.Count(stripSpace(after), t) > strings.Count(stripSpace(before), t)
}

func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

type screenState int

const (
	screenUnknown screenState = iota
	screenIdle
	screenBusy
)

func (m *Markers) classify(screen string) screenState {
	busy, idle := m.Busy.MatchString(screen), m.Idle.MatchString(screen)
	switch {
	case busy && !idle:
		return screenBusy
	case idle && !busy:
		return screenIdle
	}
	return screenUnknown
}

func (m *Markers) interrupted(screen string) bool {
	return m.Interrupted != nil && m.Interrupted.MatchString(screen)
}
