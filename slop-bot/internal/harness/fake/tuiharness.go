package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// sessionPrefix names the fake's sessions, so Adopt finds its own.
const sessionPrefix = "fake-"

// TUIHarness runs fakeharness --tui under the tuidriver kit: fake/tui.
type TUIHarness struct {
	// Binary is the built fakeharness.
	Binary string
	// Control is the Controller's socket, or "" for an autonomous fake.
	Control string
	Host    sessionhost.SessionHost
	Hub     tuidriver.HookHub
	Pointer func(msgs []*harness.Message) string
	Logger  *slog.Logger
	Clock   clock.Clock
	// PollInterval, ConfirmTimeout and RenderTimeout are
	// tuidriver.Options'; zero is the kit's default.
	PollInterval   time.Duration
	ConfirmTimeout time.Duration
	RenderTimeout  time.Duration
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
	conv, err := tuidriver.Start(ctx, h.options(SessionName(ws.Agent)), ws, resume)
	if err != nil {
		return nil, err
	}
	return conv, nil
}

// Adopt re-attaches to the fake's sessions that outlived a daemon.
func (h *TUIHarness) Adopt(ctx context.Context) ([]harness.Conversation, error) {
	names, err := h.Host.List(ctx)
	if err != nil {
		return nil, err
	}
	var convs []harness.Conversation
	for _, name := range names {
		if !strings.HasPrefix(name, sessionPrefix) {
			continue
		}
		conv, err := tuidriver.Adopt(ctx, h.options(name))
		if err != nil {
			return convs, fmt.Errorf("fake: adopting %s: %w", name, err)
		}
		convs = append(convs, conv)
	}
	return convs, nil
}

func (h *TUIHarness) options(session string) *tuidriver.Options {
	return &tuidriver.Options{
		Profile: h.Profile(), Host: h.Host, Hub: h.Hub, Session: session,
		Pointer: h.Pointer, Logger: h.Logger, Clock: h.Clock,
		PollInterval: h.PollInterval, ConfirmTimeout: h.ConfirmTimeout, RenderTimeout: h.RenderTimeout,
	}
}

// SessionName is the fake's session for an agent.
func SessionName(agent ident.AgentID) string {
	return sessionPrefix + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, string(agent))
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
		Injects:   true,
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
