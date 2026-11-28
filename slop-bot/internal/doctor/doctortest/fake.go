// Package doctortest has a fake doctor.Commands, so the checks run without
// a real harness, git or tmux.
package doctortest

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lowaa/slop-factory/slop-bot/internal/doctor"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

// Commands has the binaries a test put on its PATH and the answers it set.
// A command with no answer is an error, so a probe the test didn't expect
// shows in the report.
type Commands struct {
	mu      sync.Mutex
	paths   map[string]string
	answers map[string]answer
	calls   []string
	envs    map[string][]string
	writes  map[string][][]string
}

type answer struct {
	res *service.Result
	err error
	// serve, when set, is a scripted server, started afresh for each run.
	serve func() Server
}

// Server is a scripted interactive probe. It is given each write to its
// stdin, as lines, and returns the lines it prints in reply. A write is
// what the probe sent before reading any more output, so a server can
// refuse a message sent before the reply it should have waited for.
type Server func(write []string) []string

// NewCommands returns a fake with nothing on PATH.
func NewCommands() *Commands {
	return &Commands{paths: map[string]string{}, answers: map[string]answer{}, envs: map[string][]string{}, writes: map[string][][]string{}}
}

// OnPath puts name on the fake PATH at path.
func (c *Commands) OnPath(name, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths[name] = path
}

// Remove takes name off the fake PATH.
func (c *Commands) Remove(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.paths, name)
}

// Answer sets what a command line returns: the binary's base name and its
// args, space-joined ("codex login status").
func (c *Commands) Answer(cmdline string, res *service.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answers[cmdline] = answer{res: res}
}

// Serve makes a command line a scripted server: newServer starts one for
// each run, which sees the probe's writes (Input, then each of Then's) and
// replies to them. Writes records what it was sent.
func (c *Commands) Serve(cmdline string, newServer func() Server) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answers[cmdline] = answer{serve: newServer}
}

// Fail makes a command line fail to run at all.
func (c *Commands) Fail(cmdline string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answers[cmdline] = answer{err: err}
}

// LookPath finds name on the fake PATH.
func (c *Commands) LookPath(name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.paths[name]; ok {
		return p, nil
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// Run records the call and returns its answer. A command with a Done that
// never reports the answer fails as the real one would at its timeout.
func (c *Commands) Run(_ context.Context, cmd *doctor.Command) (*service.Result, error) {
	cmdline := strings.Join(append([]string{filepath.Base(cmd.Path)}, cmd.Args...), " ")
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, cmdline)
	c.envs[cmdline] = cmd.Env
	a, ok := c.answers[cmdline]
	switch {
	case !ok:
		return nil, fmt.Errorf("doctortest: no answer for %q", cmdline)
	case a.err != nil:
		return nil, a.err
	case a.serve != nil:
		return c.converse(cmdline, a.serve(), cmd)
	case cmd.Done != nil && !cmd.Done(a.res.Stdout):
		return nil, fmt.Errorf("doctortest: %q never gave the answer Done waits for", cmdline)
	}
	res := *a.res
	return &res, nil
}

// converse runs cmd against server as ExecCommands would: Input, then
// after each line of output Done, and Then's reply, if any, as the next
// write. c.mu is held.
func (c *Commands) converse(cmdline string, server Server, cmd *doctor.Command) (*service.Result, error) {
	var out strings.Builder
	pending := []string{cmd.Input}
	for len(pending) > 0 {
		write := pending[0]
		pending = pending[1:]
		lines := strings.Split(strings.TrimSuffix(write, "\n"), "\n")
		c.writes[cmdline] = append(c.writes[cmdline], lines)
		for _, line := range server(lines) {
			out.WriteString(line + "\n")
			if cmd.Done != nil && cmd.Done(out.String()) {
				return &service.Result{Stdout: out.String()}, nil
			}
			if cmd.Then != nil {
				if more := cmd.Then(out.String()); more != "" {
					pending = append(pending, more)
				}
			}
		}
	}
	if cmd.Done != nil {
		return nil, fmt.Errorf("doctortest: %q never gave the answer Done waits for", cmdline)
	}
	return &service.Result{Stdout: out.String()}, nil
}

// Writes are what the last runs of cmdline wrote to a scripted server, a
// write at a time.
func (c *Commands) Writes(cmdline string) [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]string(nil), c.writes[cmdline]...)
}

// CodexServer is `codex app-server` answering doctor's model list, held to
// the handshake the real one follows: `initialized` counts only when it was
// sent after initialize's reply, and model/list is refused before it.
func CodexServer() Server {
	answered, ready := false, false
	return func(write []string) []string {
		var out []string
		repliedNow := false
		for _, line := range write {
			var m struct {
				ID     *int64 `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal([]byte(line), &m) != nil {
				continue
			}
			switch {
			case m.Method == "initialize":
				out = append(out, codexInitialized, codexStatus)
				answered, repliedNow = true, true
			case m.Method == "initialized":
				// Sent in the same write as initialize, it was sent without
				// waiting for the reply.
				ready = answered && !repliedNow
			case m.Method == "model/list" && ready:
				out = append(out, codexList)
			case m.ID != nil:
				out = append(out, fmt.Sprintf(`{"id":%d,"error":{"code":-32600,"message":"Not initialized"}}`, *m.ID))
			}
		}
		return out
	}
}

// Recorded outputs of each probe on a healthy machine: the versions the
// tested ranges start at, signed in everywhere, abridged model lists.
const (
	codexInitialized = `{"id":1,"result":{"userAgent":"slop-bot-doctor/0.172.0","codexHome":"/home/u/.codex"}}`
	codexStatus      = `{"method":"remoteControl/status/changed","params":{"status":"disabled"}}`
	codexList        = `{"id":2,"result":{"data":[{"id":"gpt-6.2-sol","model":"gpt-6.2-sol","hidden":false,"isDefault":true},` +
		`{"id":"gpt-6.2-astra","model":"gpt-6.2-astra","hidden":false,"isDefault":false}],"nextCursor":null}}`
	PiModels = `provider  model                     context  max-out  thinking  images
openai    gpt-5.5                   272K     128K     yes       yes
openai    gpt-6.2-sol               272K     128K     yes       yes
`
	AgyModels = "Fetching available models...\n" +
		"gemini-4-hydrogen-high\tGemini 4 Hydrogen (High)\n" +
		"gemini-3.8-flash-low\tGemini 3.8 Flash (Low)\n"
	ClaudeSignedIn = `{"loggedIn": true, "authMethod": "claude.ai", "email": "someone@example.com"}`
	PiReady        = `{"status":"ready","provider":"openai","authType":"oauth"}`
)

// Healthy is a fake on which every check passes: each tool on PATH under
// bin, at a tested version, signed in. Repos still need worktree.CheckRepo's
// probes answered: `git -C <path> rev-parse --is-bare-repository`, then
// `--show-toplevel` (or `--absolute-git-dir` for a bare repo).
func Healthy(bin string) *Commands {
	c := NewCommands()
	for _, name := range []string{"git", "tmux", "claude", "codex", "pi", "agy"} {
		c.OnPath(name, filepath.Join(bin, name))
	}
	ok := func(stdout string) *service.Result { return &service.Result{Stdout: stdout} }
	c.Answer("git --version", ok("git version 2.55.0\n"))
	c.Answer("tmux -V", ok("tmux 3.4\n"))
	c.Answer("claude --version", ok("2.1.332 (Claude Code)\n"))
	c.Answer("claude auth status", ok(ClaudeSignedIn))
	c.Answer("codex --version", ok("codex-cli 0.172.0\n"))
	c.Answer("codex login status", ok("Logged in using ChatGPT\n"))
	c.Serve("codex app-server", CodexServer)
	c.Answer("pi --version", ok("1.4.0\n"))
	c.Answer("pi --list-models", ok(PiModels))
	c.Answer("pi auth check --model openai/gpt-6.2-sol --json --no-refresh", ok(PiReady))
	c.Answer("agy --version", ok("1.5.18\n"))
	c.Answer("agy models", ok(AgyModels))
	return c
}

// Env is the environment the command line last ran with.
func (c *Commands) Env(cmdline string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.envs[cmdline]
}

// Calls returns the command lines run so far, in order.
func (c *Commands) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}
