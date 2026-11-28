package agy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agentproc"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
)

// Hook events agy runs (agy 1.5.18). PostInvocation is left out: it says
// nothing PreInvocation and Stop don't.
const (
	HookSessionStart  = "SessionStart"
	HookPreInvocation = "PreInvocation"
	HookPreToolUse    = "PreToolUse"
	HookPostToolUse   = "PostToolUse"
	HookStop          = "Stop"
)

// efforts are what agy's --effort takes (agy --help, 1.5.18).
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// bootPrompt is the first prompt of a new conversation. agy names a
// conversation, and runs its first hook, only once it has a prompt, so a
// session launched without one has no id to resume and nothing to say it
// is ready. The turn it runs is start-up, hidden from the core (see
// tuidriver.HookCall.Events).
const bootPrompt = "You are starting up. Reply with just: ready"

// allow is PreToolUse's answer in a permissive workspace. agy refuses the
// call on an empty answer or a failed hook (the TUI spike, and this
// package's live test), so it is always given explicitly.
const allow = `{"decision":"allow"}`

// ask is PreToolUse's answer otherwise: agy's own permission prompt,
// "respects Always Allow" (agy's hook docs). A person answers it.
const ask = `{"decision":"ask"}`

// Screen markers (agy 1.5.18, the TUI spike and this package's testdata).
// The footer is the screen's last line and starts at column 0, where the
// model's own output (indented two columns) can't put the same words.
var (
	busyMarker = regexp.MustCompile(`(?m)^esc to cancel\b`)
	idleMarker = regexp.MustCompile(`(?m)^\? for shortcuts\b`)
	// Only when it is the last thing above the prompt box, which ends the
	// screen (a rule, the one-line prompt, a rule, the footer): a resumed
	// conversation shows its history, an old cancel included, and each
	// exchange in it starts with a rule and a "> " line too.
	interruptedMarker = regexp.MustCompile(`Interrupted · What should Antigravity CLI do instead\?[ \t]*\n─+[ \t]*\n>[^\n]*\n─+[ \t]*\n[^\n]*\s*\z`)
)

// Profile is agy's tuidriver profile.
func (h *Harness) Profile() *tuidriver.Profile {
	return &tuidriver.Profile{
		Harness:      Name,
		Command:      h.command,
		InstallHooks: installHooks,
		SubmitKey:    "Enter",
		CancelKey:    "Escape",
		Markers:      &tuidriver.Markers{Busy: busyMarker, Idle: idleMarker, Interrupted: interruptedMarker},
		// "press ctrl+d again to exit": keys alone, where /quit needs its
		// Enter to land after the slash-command menu opens, which it didn't
		// always under load.
		Quit:      []tuidriver.Input{{Keys: []string{"C-d", "C-d"}}},
		ParseHook: parseHook,
		Reply:     reply,
		Injects:   true,
	}
}

// command is agy's launch. Everything is session-only: --settings is never
// saved, and the sidecar is slop-bot's own directory.
func (h *Harness) command(ws *harness.Workspace, resume *harness.ConversationRef) (*harness.LaunchSpec, error) {
	bin, err := h.binary()
	if err != nil {
		return nil, err
	}
	if ws.Effort != "" && !slices.Contains(efforts, ws.Effort) {
		return nil, fmt.Errorf("agy: effort %q is not one of %v", ws.Effort, efforts)
	}
	side := sidecar(ws)
	briefing := ws.Briefing
	if before := sortsBefore(ws); len(before) > 0 {
		// agy's primary folder is the first of its folders in sorted order,
		// not its cwd: it runs commands there, and the briefing says the
		// worktree is where the agent works.
		h.logger().Warn("agy: extra folders sort before the primary worktree, and agy will work in the first",
			"agent", ws.Agent, "primary", ws.Dir, "before", before)
		briefing += fmt.Sprintf("\n\nNote: your commands start in %s, not your primary worktree. "+
			"Run every command from %s (cd there first, or give it as the command's working directory) "+
			"unless it is meant for another worktree.\n", before[0], ws.Dir)
	}
	if err := writeBriefing(side, briefing); err != nil {
		return nil, err
	}
	env := agentproc.WorkspaceEnv(h.Environ, ws, nil)
	args := []string{}
	if ws.Permissive {
		args = append(args, "--dangerously-skip-permissions")
	}
	// A new folder's trust prompt blocks the TUI, --dangerously-skip-
	// permissions or not; trusting it here skips it without writing it to
	// agy's settings.json.
	settings := map[string]any{"trustedWorkspaces": append(append([]string{ws.Dir}, ws.ExtraDirs...), side)}
	if hasAPIKey(env) {
		// agy ignores GEMINI_API_KEY unless modelProvider is gemini, which
		// its docs set in settings.json. Set here, it holds for this
		// session alone, so the user's own agy keeps its Google sign-in.
		settings["modelProvider"] = "gemini"
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	args = append(args, "--settings", string(settingsJSON), "--add-dir", side)
	for _, d := range ws.ExtraDirs {
		args = append(args, "--add-dir", d)
	}
	if ws.Model != "" {
		args = append(args, "--model", ws.Model)
	}
	if ws.Effort != "" {
		args = append(args, "--effort", ws.Effort)
	}
	if resume != nil {
		args = append(args, "--conversation", resume.ID)
	} else {
		args = append(args, "--prompt-interactive", bootPrompt)
	}
	return &harness.LaunchSpec{Path: bin, Args: args, Env: env, Dir: ws.Dir}, nil
}

// apiKeyVar is the one variable agy reads a Gemini API key from (its install
// docs: not GOOGLE_API_KEY, nor a .env file).
const apiKeyVar = "GEMINI_API_KEY" // #nosec G101 -- a variable's name, not a credential

// hasAPIKey says the agent's environment carries a Gemini API key, which it
// does only when the workspace opted in (agentproc drops it otherwise). An
// empty one doesn't count: agy refuses to start with modelProvider gemini
// and no key.
func hasAPIKey(env []string) bool {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, apiKeyVar+"="); ok && v != "" {
			return true
		}
	}
	return false
}

// sidecar is the session's own folder, added to agy's workspace with
// --add-dir: agy loads .agents/hooks.json and AGENTS.md from every folder of
// its workspace, so it carries the hooks and the briefing without writing
// either into the worktree, where an agent could commit them, or into the
// user's agy config. agy has no flag for a hooks file or a system prompt.
//
// It sits beside the worktree, with the worktree's own name as its prefix,
// because agy works in the first of its folders in sorted order: a prefix
// sorts first, so the worktree stays the primary folder.
func sidecar(ws *harness.Workspace) string {
	return filepath.Clean(ws.Dir) + ".slop-bot-agy"
}

// sortsBefore are ws's extra folders that sort before the worktree, first
// the one agy takes as primary.
func sortsBefore(ws *harness.Workspace) []string {
	var out []string
	for _, d := range ws.ExtraDirs {
		if d < ws.Dir {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

// writeBriefing writes the briefing as the sidecar's AGENTS.md, which agy
// reads as instructions for every model call: it holds across resume,
// compaction and a person attaching, where a message injected once would
// not.
func writeBriefing(side, briefing string) error {
	if err := os.MkdirAll(filepath.Join(side, ".agents"), 0o700); err != nil {
		return err
	}
	path := filepath.Join(side, "AGENTS.md")
	if briefing == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(briefing), 0o600)
}

// hookSpec is one handler in agy's hooks.json.
type hookSpec struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// toolHookGroup is a tool event's handlers, for the tools its matcher
// (an anchored regexp on the tool name) names.
type toolHookGroup struct {
	Matcher string     `json:"matcher"`
	Hooks   []hookSpec `json:"hooks"`
}

// installHooks writes the sidecar's .agents/hooks.json: one named hook,
// running the bridge for each event. agy runs a hook's command through the
// shell.
func installHooks(ws *harness.Workspace, _ *harness.LaunchSpec, hook []string) error {
	events := map[string]any{}
	for _, ev := range []string{HookSessionStart, HookPreInvocation, HookStop} {
		events[ev] = []hookSpec{{Type: "command", Command: hookCommand(hook, ev)}}
	}
	pre := hookCommand(hook, HookPreToolUse)
	if !ws.Permissive {
		pre = askCommand(hook)
	}
	events[HookPreToolUse] = []toolHookGroup{{Matcher: ".*", Hooks: []hookSpec{{Type: "command", Command: pre}}}}
	events[HookPostToolUse] = []toolHookGroup{{Matcher: ".*", Hooks: []hookSpec{{Type: "command", Command: hookCommand(hook, HookPostToolUse)}}}}
	b, err := json.MarshalIndent(map[string]any{"slop-bot": events}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(sidecar(ws), ".agents", "hooks.json"), b, 0o600)
}

// hookCommand runs the bridge, answering for it when it fails. A hook that
// fires while slop-bot is unreachable (the daemon restarting, say) must
// still answer: agy reads a failed PreToolUse as a refusal, which would
// fail the agent's tool calls for as long as the daemon is away. The
// fallback says nothing beyond agy's defaults, so nothing is lost but the
// hook's news, which the screen and transcript carry too.
func hookCommand(hook []string, event string) string {
	return bridge(hook, event) + " || printf '%s' " + shellQuote(defaultReply(event))
}

// askCommand is a non-permissive workspace's PreToolUse: slop-bot still
// hears of the call, but agy's own prompt decides it, whatever the bridge
// answers or however it fails, so no answer of slop-bot's can widen the
// user's permissions.
func askCommand(hook []string) string {
	return bridge(hook, HookPreToolUse) + " >/dev/null 2>&1; printf '%s' " + shellQuote(ask)
}

func bridge(hook []string, event string) string {
	argv := append(slices.Clone(hook), event)
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

func defaultReply(event string) string {
	if event == HookPreToolUse {
		return allow
	}
	return "{}"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// payload is what agy passes every hook, as far as slop-bot reads it.
type payload struct {
	ConversationID string `json:"conversationId"`
	// ToolCall is on PreToolUse and PostToolUse.
	ToolCall *struct {
		Name string `json:"name"`
	} `json:"toolCall"`
	// Error is on PostToolUse and Stop.
	Error string `json:"error"`
	// TerminationReason is on Stop: NO_TOOL_CALL for a turn that ended
	// normally; also ERROR, USER_CANCELED, MAX_INVOCATIONS and others
	// (agy's TERMINATION_REASON_* names, without the prefix).
	TerminationReason string `json:"terminationReason"`
}

func parseHook(event string, data []byte) (*tuidriver.HookCall, error) {
	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	call := &tuidriver.HookCall{ConversationID: p.ConversationID}
	switch event {
	case HookSessionStart:
		// Only a new conversation's, at its first prompt: the boot prompt.
		call.Submitted = true
	case HookPreInvocation:
		// Before every model call; the first of a turn starts it.
		call.Events = []harness.Event{{Kind: harness.TurnStarted}}
		call.Submitted = true
		call.Inject = tuidriver.InjectMidTurn
	case HookPreToolUse:
		call.Events = []harness.Event{{Kind: harness.ToolStarted, Tool: toolName(&p)}}
	case HookPostToolUse:
		call.Events = []harness.Event{{Kind: harness.ToolEnded, Tool: toolName(&p)}}
	case HookStop:
		ended := harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnCompleted}
		switch {
		case p.Error != "" || p.TerminationReason == "ERROR":
			ended.Outcome, ended.Text = harness.TurnFailed, p.Error
		case p.TerminationReason == "USER_CANCELED":
			ended.Outcome = harness.TurnInterrupted
		default:
			// Only a turn that ended on its own is kept going: a failure
			// must reach the core, and a cancel must stay cancelled.
			call.Inject = tuidriver.InjectTurnEnd
		}
		// Ready: the boot prompt's turn has ended (after that, a repeat).
		call.Events = []harness.Event{ended, {Kind: harness.Ready}}
	default:
		return nil, fmt.Errorf("agy: unknown hook %q", event)
	}
	return call, nil
}

func toolName(p *payload) string {
	if p.ToolCall == nil {
		return ""
	}
	return p.ToolCall.Name
}

type injectStep struct {
	SystemMessage struct {
		SystemMessage string `json:"systemMessage"`
	} `json:"systemMessage"`
}

type injectReply struct {
	InjectSteps []injectStep `json:"injectSteps"`
}

type stopReply struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// reply answers every hook: PreToolUse always allows, PreInvocation carries
// messages as injected system steps, and Stop carries them as a continue,
// whose reason agy gives the model.
func reply(call *tuidriver.HookCall, inject []*harness.Message) ([]byte, error) {
	switch {
	case call.Event == HookPreToolUse:
		return []byte(allow), nil
	case len(inject) == 0:
		return []byte("{}"), nil
	case call.Inject == tuidriver.InjectMidTurn:
		var r injectReply
		for _, m := range inject {
			var s injectStep
			s.SystemMessage.SystemMessage = m.Text
			r.InjectSteps = append(r.InjectSteps, s)
		}
		return json.Marshal(r)
	case call.Inject == tuidriver.InjectTurnEnd:
		texts := make([]string, len(inject))
		for i, m := range inject {
			texts[i] = m.Text
		}
		return json.Marshal(stopReply{Decision: "continue", Reason: strings.Join(texts, "\n\n")})
	}
	return nil, fmt.Errorf("agy: %s can't carry messages", call.Event)
}

func (h *Harness) binary() (string, error) {
	if h.Binary != "" {
		return h.Binary, nil
	}
	return exec.LookPath("agy")
}
