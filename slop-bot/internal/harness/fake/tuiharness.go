package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// sessionPrefix names the fake's sessions.
const sessionPrefix = "fake-"

// TUIHarness runs fakeharness --tui under the tuidriver kit: fake/tui.
type TUIHarness struct {
	tuidriver.Sessions
	// Binary is the built fakeharness.
	Binary string
	// Control is the Controller's socket, or "" for an autonomous fake.
	Control string
}

// Name implements harness.AIHarness.
func (h *TUIHarness) Name() string { return Name }

// Driver implements harness.AIHarness.
func (h *TUIHarness) Driver() string { return tuidriver.Driver }

// Capabilities implements harness.AIHarness: the step hook injects mid-turn.
func (h *TUIHarness) Capabilities() harness.Capabilities {
	return harness.Capabilities{MidTurnSteer: true}
}

// Start implements harness.AIHarness.
func (h *TUIHarness) Start(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) (harness.Conversation, error) {
	return h.Sessions.Start(ctx, h.Profile(), sessionPrefix, ws, resume)
}

// AdoptAgent implements harness.Adopter.
func (h *TUIHarness) AdoptAgent(ctx context.Context, agent ident.AgentID, known string) (harness.Conversation, error) {
	return h.Adopt(ctx, h.Profile(), sessionPrefix, agent, known)
}

// SessionName is the fake's session for an agent.
func SessionName(agent ident.AgentID) string {
	return tuidriver.SessionName(sessionPrefix, agent)
}

// Profile is the fake TUI's tuidriver profile.
func (h *TUIHarness) Profile() *tuidriver.Profile {
	return &tuidriver.Profile{
		Harness: Name,
		Command: func(ws *harness.Workspace, resume *harness.ConversationRef) (*harness.LaunchSpec, error) {
			return launch(h.Binary, h.Control, "", ws, resume, "--tui"), nil
		},
		InstallHooks: installHooks,
		SubmitKey:    "Enter",
		CancelKey:    "Escape",
		Markers: &tuidriver.Markers{
			Busy:        regexp.MustCompile(regexp.QuoteMeta(footerBusy)),
			Idle:        regexp.MustCompile(regexp.QuoteMeta(footerIdle)),
			Interrupted: regexp.MustCompile(`(?m)^interrupted\s*$`),
		},
		Quit:      []tuidriver.Input{{Text: "/quit", Keys: []string{"Enter"}}},
		ParseHook: parseHook,
		Reply:     replyHook,
		Deny: func(reason string) []byte {
			b, _ := json.Marshal(HookReply{Deny: reason})
			return b
		},
		Injects: true,
	}
}

// installHooks is session-only: the hook command goes in the session's
// environment.
func installHooks(_ *harness.Workspace, spec *harness.LaunchSpec, hook []string) error {
	b, err := json.Marshal(hook)
	if err != nil {
		return err
	}
	spec.Env = append(spec.Env, EnvHookCmd+"="+string(b))
	return nil
}

func parseHook(event string, payload []byte) (*tuidriver.HookCall, error) {
	var p HookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	call := &tuidriver.HookCall{ConversationID: p.ConversationID}
	switch event {
	case HookSessionStart:
		call.Events = []harness.Event{{Kind: harness.Ready}}
	case HookPromptSubmit:
		call.Submitted = true
	case HookTurnStart:
		call.Events = []harness.Event{{Kind: harness.TurnStarted}}
	case HookTurnEnd:
		call.Events = []harness.Event{{Kind: harness.TurnEnded, Outcome: harness.TurnOutcome(p.Outcome)}}
		// A cancelled turn can't be kept going.
		if p.Outcome != string(harness.TurnInterrupted) {
			call.Inject = tuidriver.InjectTurnEnd
		}
	case HookToolStart:
		call.Events = []harness.Event{{Kind: harness.ToolStarted, Tool: p.Tool}}
		// The call's input is its command line, as for a shell tool.
		call.Tool = &tuidriver.ToolCall{Name: p.Tool, Command: p.Text}
	case HookToolEnd:
		call.Events = []harness.Event{{Kind: harness.ToolEnded, Tool: p.Tool}}
	case HookText:
		call.Events = []harness.Event{{Kind: harness.Text, Text: p.Text}}
	case HookUsage:
		call.Events = []harness.Event{{Kind: harness.UsageReport, Usage: &harness.Usage{InputTokens: p.Input, OutputTokens: p.Output}}}
	case HookStep:
		call.Inject = tuidriver.InjectMidTurn
	default:
		return nil, fmt.Errorf("fake: unknown hook %q", event)
	}
	return call, nil
}

func replyHook(call *tuidriver.HookCall, inject []*harness.Message) ([]byte, error) {
	reply := HookReply{Continue: call.Inject == tuidriver.InjectTurnEnd && len(inject) > 0}
	for _, m := range inject {
		reply.Inject = append(reply.Inject, InjectedMessage{From: m.From.String(), Text: m.Text})
	}
	return json.Marshal(reply)
}
