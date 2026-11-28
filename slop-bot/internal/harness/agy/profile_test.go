package agy

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
)

// The test binary doubles as the fake harness, whose hook relay stands in
// for `slop-bot hook`.
func TestMain(m *testing.M) { faketest.Main(m) }

const conversationID = "d013e0e5-55a8-4f9f-b5e5-b04270cabbff"

func testdata(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", path))
	require.NoError(t, err)
	return b
}

// The screens were captured from agy 1.5.18 in tmux (capture-pane -J).
func TestScreenMarkers(t *testing.T) {
	t.Parallel()
	// An old cancel the resumed conversation's history shows, with a later
	// exchange below it: no longer what the screen is about.
	resumed := string(testdata(t, "screens/resumed.txt"))
	const cancelled = "  ⎿  Interrupted · What should Antigravity CLI do instead?\n"
	require.Contains(t, resumed, cancelled)
	scrolled := strings.Replace(resumed, cancelled, cancelled+strings.Repeat("─", 60)+"\n> carry on\n  done\n", 1)

	m := (&Harness{}).Profile().Markers
	for name, want := range map[string]struct {
		screen                  string
		busy, idle, interrupted bool
	}{
		"idle at a new prompt": {screen: string(testdata(t, "screens/boot-idle.txt")), idle: true},
		"running a tool":       {screen: string(testdata(t, "screens/busy-tool.txt")), busy: true},
		// The footer gives way to the queue's hint: neither, so the hooks
		// decide.
		"busy with a message queued": {screen: string(testdata(t, "screens/busy-queued.txt"))},
		// The queued message moved into the prompt, which hides the footer.
		"cancelled":                       {screen: string(testdata(t, "screens/interrupted.txt")), interrupted: true},
		"resumed after a cancel":          {screen: resumed, idle: true, interrupted: true},
		"an old cancel, scrolled up":      {screen: scrolled, idle: true},
		"the model quoting the footer":    {screen: "  esc to cancel\n  ? for shortcuts\n"},
		"the model quoting the cancel":    {screen: "  Interrupted · What should Antigravity CLI do instead?\n  > no\n"},
		"a first launch's dismiss notice": {screen: " ┃ esc to dismiss\n"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want.busy, m.Busy.MatchString(want.screen), "busy")
			assert.Equal(t, want.idle, m.Idle.MatchString(want.screen), "idle")
			assert.Equal(t, want.interrupted, m.Interrupted.MatchString(want.screen), "interrupted")
		})
	}
}

// The payloads were captured from agy 1.5.18's TUI, run with hooks that
// logged them.
func TestParseHook(t *testing.T) {
	t.Parallel()
	for event, want := range map[string]*tuidriver.HookCall{
		HookSessionStart: {ConversationID: conversationID, Submitted: true},
		HookPreInvocation: {
			ConversationID: conversationID, Submitted: true, Inject: tuidriver.InjectMidTurn,
			Events: []harness.Event{{Kind: harness.TurnStarted}},
		},
		HookPreToolUse: {
			ConversationID: conversationID,
			Events:         []harness.Event{{Kind: harness.ToolStarted, Tool: "run_command"}},
		},
		HookPostToolUse: {
			ConversationID: conversationID,
			Events:         []harness.Event{{Kind: harness.ToolEnded, Tool: "run_command"}},
		},
		HookStop: {
			ConversationID: conversationID, Inject: tuidriver.InjectTurnEnd,
			Events: []harness.Event{{Kind: harness.TurnEnded, Outcome: harness.TurnCompleted}, {Kind: harness.Ready}},
		},
	} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			got, err := parseHook(event, testdata(t, "hooks/"+event+".json"))
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestParseHookFailedTurnAndUnknownEvent(t *testing.T) {
	t.Parallel()
	got, err := parseHook(HookStop, []byte(`{"conversationId":"c1","error":"quota exhausted"}`))
	require.NoError(t, err)
	assert.Equal(t, harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnFailed, Text: "quota exhausted"}, got.Events[0])

	_, err = parseHook("PostInvocation", []byte(`{}`))
	assert.Error(t, err, "not installed, so never expected")
	_, err = parseHook(HookStop, []byte(`not json`))
	assert.Error(t, err)
}

func TestReply(t *testing.T) {
	t.Parallel()
	msgs := []*harness.Message{{Text: "main is broken"}, {Text: "stop merging"}}
	for name, tc := range map[string]struct {
		call   *tuidriver.HookCall
		inject []*harness.Message
		want   string
	}{
		// agy denies a tool call on any answer but an explicit allow.
		"a tool call is allowed": {call: &tuidriver.HookCall{Event: HookPreToolUse}, want: allow},
		"nothing to say":         {call: &tuidriver.HookCall{Event: HookPostToolUse}, want: `{}`},
		"nothing to inject":      {call: &tuidriver.HookCall{Event: HookPreInvocation, Inject: tuidriver.InjectMidTurn}, want: `{}`},
		// The shapes the TUI spike saw agy act on.
		"mid-turn messages": {
			call: &tuidriver.HookCall{Event: HookPreInvocation, Inject: tuidriver.InjectMidTurn}, inject: msgs,
			want: `{"injectSteps":[{"systemMessage":{"systemMessage":"main is broken"}},{"systemMessage":{"systemMessage":"stop merging"}}]}`,
		},
		"turn-end messages": {
			call: &tuidriver.HookCall{Event: HookStop, Inject: tuidriver.InjectTurnEnd}, inject: msgs,
			want: `{"decision":"continue","reason":"main is broken\n\nstop merging"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := reply(tc.call, tc.inject)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
	_, err := reply(&tuidriver.HookCall{Event: HookSessionStart}, msgs)
	assert.Error(t, err, "the kit never injects where the profile says it can't")
}

func workspace(t *testing.T) *harness.Workspace {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "t7-repo")
	require.NoError(t, os.Mkdir(dir, 0o700))
	return &harness.Workspace{Agent: "a1", Dir: dir, Permissive: true, Briefing: "Be terse."}
}

func TestCommand(t *testing.T) {
	t.Parallel()
	h := &Harness{Binary: "/bin/agy", Environ: func() []string { return []string{"HOME=/home/u", "GEMINI_API_KEY=k", "CLAUDE_CODE_SESSION_ID=s"} }}
	ws := workspace(t)
	ws.ExtraDirs = []string{ws.Dir + "-lib"}
	ws.Model, ws.Effort = "gemini-4-hydrogen-low", "low"
	side := ws.Dir + ".slop-bot-agy"
	settings := `{"trustedWorkspaces":["` + ws.Dir + `","` + ws.Dir + `-lib","` + side + `"]}`

	spec, err := h.command(ws, nil)
	require.NoError(t, err)
	assert.Equal(t, "/bin/agy", spec.Path)
	assert.Equal(t, ws.Dir, spec.Dir)
	assert.Equal(t, []string{
		"--dangerously-skip-permissions", "--settings", settings,
		"--add-dir", side, "--add-dir", ws.Dir + "-lib",
		"--model", "gemini-4-hydrogen-low", "--effort", "low",
		"--prompt-interactive", bootPrompt,
	}, spec.Args)
	assert.Equal(t, []string{"HOME=/home/u"}, spec.Env, "no API key unless opted in, and no parent session's variables")
	briefing, err := os.ReadFile(filepath.Join(side, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "Be terse.", string(briefing))

	resumed, err := h.command(ws, &harness.ConversationRef{ID: "c1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"--conversation", "c1"}, resumed.Args[len(resumed.Args)-2:], "no boot prompt: the conversation has its id")

	ws.Briefing, ws.Permissive = "", false
	plain, err := h.command(ws, nil)
	require.NoError(t, err)
	assert.NotContains(t, plain.Args, "--dangerously-skip-permissions")
	assert.NoFileExists(t, filepath.Join(side, "AGENTS.md"), "no stale briefing")

	ws.Effort = "extreme"
	_, err = h.command(ws, nil)
	assert.ErrorContains(t, err, "effort")
}

// agy works in the first of its folders in sorted order (probed with
// --add-dir), so the sidecar must sort after the worktree it serves.
func TestTheSidecarSortsAfterItsWorktree(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"/w/t7-repo", "/w/t7-repo/", "/w/t7-a", "/w/t7-repo~"} {
		ws := &harness.Workspace{Dir: dir}
		assert.Less(t, filepath.Clean(dir), sidecar(ws), dir)
		assert.NotEqual(t, filepath.Clean(dir), filepath.Dir(sidecar(ws)), "outside the worktree")
	}
	assert.Equal(t, []string{"/w/t7-a"}, sortsBefore(&harness.Workspace{Dir: "/w/t7-repo", ExtraDirs: []string{"/w/t7-a", "/w/t7-z"}}))
}

func installed(t *testing.T, hook []string) (*harness.Workspace, map[string]json.RawMessage) {
	t.Helper()
	ws := workspace(t)
	h := &Harness{Binary: "/bin/agy"}
	spec, err := h.command(ws, nil)
	require.NoError(t, err)
	require.NoError(t, installHooks(ws, spec, hook))
	var file map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(readFile(t, filepath.Join(sidecar(ws), ".agents", "hooks.json")), &file))
	require.Len(t, file, 1, "one named hook")
	return ws, file["slop-bot"]
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- the test's own temp file
	require.NoError(t, err)
	return b
}

// commandFor is the shell command hooks.json gives event, in agy's layout:
// a tool event's handlers sit in matcher groups, the others' directly.
func commandFor(t *testing.T, events map[string]json.RawMessage, event string) string {
	t.Helper()
	raw, ok := events[event]
	require.True(t, ok, "%s is installed", event)
	if event == HookPreToolUse || event == HookPostToolUse {
		var groups []toolHookGroup
		require.NoError(t, json.Unmarshal(raw, &groups))
		require.Len(t, groups, 1)
		assert.Equal(t, ".*", groups[0].Matcher, "every tool")
		require.Len(t, groups[0].Hooks, 1)
		return groups[0].Hooks[0].Command
	}
	var specs []hookSpec
	require.NoError(t, json.Unmarshal(raw, &specs))
	require.Len(t, specs, 1)
	assert.Equal(t, "command", specs[0].Type)
	return specs[0].Command
}

// runHook runs a hook command the way agy does, through the shell, with a
// payload on stdin.
func runHook(t *testing.T, command string, env []string) string {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", command) // #nosec G204 -- the test's own hook
	cmd.Env = env
	cmd.Stdin = strings.NewReader(`{"conversationId":"c1"}`)
	out, err := cmd.Output()
	require.NoError(t, err, "a hook must never fail: agy reads a failed PreToolUse as a refusal")
	return string(out)
}

// The bridge relays a hook to slop-bot and back; when slop-bot can't be
// reached, the hook still answers, with agy's defaults.
func TestTheHookBridgeAnswersWhenSlopBotIsAway(t *testing.T) {
	t.Parallel()
	self, err := os.Executable()
	require.NoError(t, err)
	hub, err := fake.NewHookHub(self)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hub.Close() })
	// A session name with a quote, which the shell must see as one word.
	const session = "agy-a'1"
	_, events := installed(t, hub.Command(session))
	assert.Len(t, events, 5)
	env := append(os.Environ(), "SLOP_BOT_FAKE_REEXEC=1")

	got := make(chan string, 1)
	unroute, err := hub.Route(session, func(_ context.Context, event string, payload []byte) ([]byte, error) {
		got <- event + " " + string(payload)
		return []byte(`{"from":"slop-bot"}`), nil
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"from":"slop-bot"}`, runHook(t, commandFor(t, events, HookStop), env))
	assert.Equal(t, `Stop {"conversationId":"c1"}`, <-got)

	// Routed nowhere: the daemon restarting, or this session not yet routed.
	unroute()
	assert.JSONEq(t, allow, runHook(t, commandFor(t, events, HookPreToolUse), env))
	for _, ev := range []string{HookSessionStart, HookPreInvocation, HookPostToolUse, HookStop} {
		assert.JSONEq(t, `{}`, runHook(t, commandFor(t, events, ev), env), ev)
	}
	// slop-bot gone altogether.
	require.NoError(t, hub.Close())
	assert.JSONEq(t, allow, runHook(t, commandFor(t, events, HookPreToolUse), env))
}
