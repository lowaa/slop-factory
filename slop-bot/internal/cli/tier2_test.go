package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	"github.com/lowaa/slop-factory/slop-bot/internal/gitx/gitxtest"
	sbharness "github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance/scripted"
	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
)

// Tier 2 through the daemon: the tier-1 setup (a real daemon in-process, the
// real CLI re-exec'd from this test binary) with a real harness CLI in place
// of the fake, pointed at a scripted model. The model's script makes the
// agent's tool calls, so the agent runs slop-bot from its own shell, with
// its own token, as it would in production. Opt-in (scripted.OptIn); tier2.sh
// runs it.

// tier2Wait bounds each wait: a safety net for a stuck harness, never a
// synchronisation. Each step takes a second or two when it works; a real
// harness under -race, many at once, starts slower than the fake.
const tier2Wait = 2 * time.Minute

// tier2Target is one harness the scenarios run on.
type tier2Target struct {
	// name is the harness's, as the config and tickets spell it.
	name string
	// bin is its CLI on PATH.
	bin    string
	target func(*testing.T, *scripted.TargetOptions) *scripted.Target
	tui    *tuiScript
}

var tier2Targets = []*tier2Target{
	{name: "claude-code", bin: "claude", target: scripted.ClaudeCode, tui: claudeTUI},
	{name: "codex", bin: "codex", target: scripted.Codex, tui: codexTUI},
	{name: "pi", bin: "pi", target: scripted.Pi, tui: piTUI},
}

// tier2Harness is a real harness as the daemon runs it: each workspace
// gets the target's variables and the ones that make the agent's slop-bot
// calls reach this test's daemon, and each conversation started is
// reported.
type tier2Harness struct {
	sbharness.AIHarness
	env    map[string]string
	starts chan *tier2Start
}

// tier2Start is a conversation the daemon started.
type tier2Start struct {
	id      string
	resumed bool
	// dir is its cwd: the primary worktree.
	dir string
}

// Transcripts passes on the adapter's reader, which embedding the
// interface would hide: the daemon finds one by asserting for it.
func (h *tier2Harness) Transcripts() transcript.Reader {
	return h.AIHarness.(transcript.Provider).Transcripts()
}

func (h *tier2Harness) Start(ctx context.Context, ws *sbharness.Workspace, resume *sbharness.ConversationRef) (sbharness.Conversation, error) {
	for k, v := range h.env {
		ws.Env[k] = v
	}
	conv, err := h.AIHarness.Start(ctx, ws, resume)
	if err != nil {
		return nil, err
	}
	h.starts <- &tier2Start{id: conv.Ref().ID, resumed: resume != nil, dir: ws.Dir}
	return conv, nil
}

// t2 is one scenario's world: a daemon whose only harness is the target,
// its scripted model, and a repo for the ticket's worktree.
type t2 struct {
	*e2e
	target  *scripted.Target
	harness *tier2Harness
	server  *scriptedmodel.Server
	// fifos holds the long commands' FIFOs.
	fifos string
	longs int
	// briefing is in the user briefing, so in every request the model is
	// sent: the check that the briefing reached the agent.
	briefing string
	// tui is how to get the target's TUI through an attach.
	tui *tuiScript
	// workspace is where the daemon puts worktrees.
	workspace string
	// gh is the stub gh, run by its path: a harness's shell may not keep
	// the PATH it was given (in the tier 2 image, Codex's commands didn't
	// find it on PATH). The guard matches a command by its basename.
	gh string
}

// ghStub stands in for gh, so a merge the guard lets through runs
// something harmless and says so.
const ghStub = "#!/bin/sh\necho \"GH-STUB-RAN: $*\"\n"

func newT2(t *testing.T, tt *tier2Target, bin string) *t2 {
	t.Helper()
	server, err := scriptedmodel.New(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	require.NoError(t, server.Load(&scriptedmodel.Scenario{Name: tt.name, Turns: []*scriptedmodel.Turn{}}))

	h := newHarness(t)
	self, err := os.Executable()
	require.NoError(t, err)
	stubs := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(stubs, "gh"), []byte(ghStub), 0o700)) // #nosec G306 -- an executable stub
	target := tt.target(t, &scripted.TargetOptions{
		Binary: bin, ServerURL: server.URL(),
		PreToolHook: []string{self, "hook", "pre-tool"},
	})
	env := map[string]string{
		// The agent's slop-bot (its tool calls, and the merge guard's hook)
		// is this test binary, as the CLI; the daemon is at its runtime
		// dir. HOME stays the target's: pi's launcher needs the real one.
		cliReexec: "1", "XDG_RUNTIME_DIR": h.runtime, "GORACE": "atexit_sleep_ms=0",
		"XDG_CONFIG_HOME": t.TempDir(), "XDG_STATE_HOME": t.TempDir(), "XDG_DATA_HOME": t.TempDir(), "XDG_CACHE_HOME": t.TempDir(),
	}
	for k, v := range target.Env {
		env[k] = v
	}
	wrapped := &tier2Harness{AIHarness: target.Harness, env: env, starts: make(chan *tier2Start, 16)}
	h.deps.harnesses = daemon.Harnesses{tt.name: wrapped}

	b := &t2{gh: filepath.Join(stubs, "gh"), target: target, harness: wrapped, server: server, fifos: t.TempDir(), briefing: "TIER2-BRIEFING-" + tt.name + "-9d4e", tui: tt.tui,
		workspace: filepath.Join(t.TempDir(), "workspace")}
	briefing := filepath.Join(h.home, "briefing.md")
	require.NoError(t, os.WriteFile(briefing, []byte(b.briefing+"\n"), 0o600))
	require.NoError(t, os.WriteFile(h.config, []byte(b.config(t, tt.name, briefing)), 0o600))

	// Registered before the daemon starts, so it runs once the daemon (and
	// with it every harness) has stopped: nothing can ask anything after.
	t.Cleanup(b.checkRecording(t))
	b.e2e = &e2e{t: t, h: h, self: self, stop: startDaemon(t, h), timeout: tier2Wait}
	return b
}

// config is the daemon's: the target as its one harness, with the model
// and key opt-in the production config would name, and one repo.
func (b *t2) config(t *testing.T, name, briefing string) string {
	t.Helper()
	cfg := map[string]any{
		"user":            "ada",
		"default_harness": name,
		"workspace_dir":   b.workspace,
		"briefing_path":   briefing,
		"repos":           []any{map[string]any{"name": "app", "path": newRepo(t)}},
		"harnesses": map[string]any{name: map[string]any{
			"model": b.target.Model, "api_key_opt_in": b.target.APIKeyOptIn,
		}},
	}
	// JSON is YAML: no quoting rules to get wrong for temp paths.
	out, err := json.Marshal(cfg)
	require.NoError(t, err)
	return string(out)
}

// newRepo is a clone of a bare "remote" with one commit pushed, as after a
// real clone: a worktree with nothing new on it holds no work, so it is
// cleaned up when the ticket ends.
func newRepo(t *testing.T) string {
	t.Helper()
	git := gitxtest.New(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	bare, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "app")
	run := func(dir string, args ...string) {
		_, err := git.Run(context.Background(), dir, args...)
		require.NoError(t, err)
	}
	run(root, "init", "-q", "--bare", bare)
	run(root, "init", "-q", repo)
	run(repo, "remote", "add", "origin", bare)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README"), []byte("app\n"), 0o600))
	run(repo, "add", "README")
	run(repo, "commit", "-q", "-m", "init")
	run(repo, "push", "-q", "-u", "origin", "main")
	run(repo, "remote", "set-head", "origin", "--auto")
	return repo
}

// checkRecording is the scenario's last word on the model: nothing
// unexpected was asked, every scripted turn was played, and every request
// carried the briefing.
func (b *t2) checkRecording(t *testing.T) func() {
	return func() {
		rec := b.server.Recording()
		assert.Empty(t, rec.Errors, "the scripted model saw unexpected requests")
		assert.Zero(t, b.server.Remaining(), "scripted turns were left unplayed: %s", summariseRequests(rec.Conversation()))
		for i, r := range rec.Conversation() {
			assert.Contains(t, r.System+"\n"+r.MessageText, b.briefing, "request %d did not carry the briefing", i)
		}
	}
}

// script appends turns to the model's script.
func (b *t2) script(turns ...*scriptedmodel.Turn) {
	b.t.Helper()
	require.NoError(b.t, b.server.Append(turns...))
}

// slopBot is a turn in which the model runs slop-bot with args in its shell,
// by the path the briefing names: this test binary, which is the CLI with
// the agent's environment.
func (b *t2) slopBot(args ...string) *scriptedmodel.Turn {
	return b.shell(shellWords(append([]string{b.self}, args...)...))
}

// shell is a turn in which the model runs cmd in its shell tool.
func (b *t2) shell(cmd string) *scriptedmodel.Turn {
	return &scriptedmodel.Turn{
		ToolCalls: []*scriptedmodel.ToolCall{{Name: b.target.Shell.Tool, Input: b.target.Shell.Input(cmd)}},
		Usage:     scriptedmodel.Usage{InputTokens: 10, OutputTokens: 4},
	}
}

func reply(text string) *scriptedmodel.Turn {
	return &scriptedmodel.Turn{Text: text, Usage: scriptedmodel.Usage{InputTokens: 10, OutputTokens: 2}}
}

// long is a shell command that runs until it is released.
func (b *t2) long() *scripted.LongCommand {
	b.t.Helper()
	b.longs++
	l, err := scripted.NewLongCommand(b.fifos, b.longs)
	require.NoError(b.t, err)
	b.t.Cleanup(l.Unblock)
	return l
}

// create files an assigned ticket for the target on the app repo, and
// returns it with its agent's first conversation.
func (b *t2) create(title string) (api.Ticket, *tier2Start) {
	b.t.Helper()
	tk := b.h.ticket(b.t, "ticket", "create", "--title", title, "--harness", b.target.Name, "--repo", "app", "--assign")
	return tk, b.started(false)
}

// createForAttach is create for a ticket whose agent a person will attach
// to: the ticket is filed first and assigned once the TUI's preparation for
// its worktree is done, before any harness session runs there.
func (b *t2) createForAttach(title string) (api.Ticket, *tier2Start) {
	b.t.Helper()
	tk := b.h.ticket(b.t, "ticket", "create", "--title", title, "--harness", b.target.Name, "--repo", "app")
	if b.tui.prepare != nil {
		// The worktree's name is worktree.dirName's: the start's own dir
		// is checked against it below.
		b.tui.prepare(b.t, b.target, filepath.Join(b.workspace, "t"+tk.ID+"-app"))
	}
	b.h.ok(b.t, "ticket", "assign", tk.ID)
	start := b.started(false)
	require.Equal(b.t, filepath.Join(b.workspace, "t"+tk.ID+"-app"), start.dir, "the worktree prepared for")
	return tk, start
}

// started waits for the daemon to start a conversation.
func (b *t2) started(resumed bool) *tier2Start {
	b.t.Helper()
	select {
	case s := <-b.harness.starts:
		require.Equal(b.t, resumed, s.resumed, "a resume")
		return s
	case <-b.ctx().Done():
		require.FailNow(b.t, "no conversation started")
		return nil
	}
}

// settled waits for the agent's turn to settle and checks where: the first
// state a turn leaves an agent in, so a scenario that went wrong fails at
// once, with what the model was sent, rather than at its deadline.
func (b *t2) settled(want string) *api.Event {
	b.t.Helper()
	ev := b.await("agent.state", func(ev *api.Event) bool {
		var p struct{ To string }
		_ = json.Unmarshal(ev.Payload, &p)
		switch p.To {
		case "waiting_user", "waiting_merge", "discussion", "idle", "stalled", "crashed", "exited", "done", "released":
			return true
		}
		return false
	})
	require.Contains(b.t, string(ev.Payload), `"to":"`+want+`"`, "%s\n%s", ev.Payload, b.requests())
	return ev
}

// finished waits for the agent to end with its ticket done, and checks its
// worktree was cleaned up.
func (b *t2) finished(tk api.Ticket, worktree string) {
	b.t.Helper()
	b.await("agent.finished", nil)
	assert.Equal(b.t, "done", b.h.ticket(b.t, "ticket", "show", tk.ID).Status)
	assert.NoDirExists(b.t, worktree, "the worktree was cleaned up")
}

// requests are what the model was sent, for a failure's context.
func (b *t2) requests() string {
	return summariseRequests(b.server.Recording().Conversation())
}

// firstWith is the index of the first conversation request whose text
// holds s, and those requests.
func (b *t2) firstWith(s string) (int, []*scriptedmodel.Request) {
	reqs := b.server.Recording().Conversation()
	for i, r := range reqs {
		if strings.Contains(r.MessageText, s) {
			return i, reqs
		}
	}
	return -1, reqs
}

// lastResults are the tool results of the latest request: every one so
// far, in order, since a harness sends the whole history each time.
func (b *t2) lastResults() []string {
	reqs := b.server.Recording().Conversation()
	if len(reqs) == 0 {
		return nil
	}
	return reqs[len(reqs)-1].ToolResults
}

func summariseRequests(reqs []*scriptedmodel.Request) string {
	parts := make([]string, len(reqs))
	for i, r := range reqs {
		text := r.MessageText
		if len(text) > 300 {
			text = "..." + text[len(text)-300:]
		}
		parts[i] = fmt.Sprintf("request %d: tool results %q, text %q", i, r.ToolResults, text)
	}
	return fmt.Sprintf("%d requests:\n%s", len(reqs), strings.Join(parts, "\n"))
}

// shellWords is args as one POSIX shell command line.
func shellWords(args ...string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}
