// Package doctortest has a fake doctor.Commands, so the checks run without
// a real harness, git or tmux.
package doctortest

import (
	"context"
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
}

type answer struct {
	res *service.Result
	err error
}

// NewCommands returns a fake with nothing on PATH.
func NewCommands() *Commands {
	return &Commands{paths: map[string]string{}, answers: map[string]answer{}}
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
	a, ok := c.answers[cmdline]
	switch {
	case !ok:
		return nil, fmt.Errorf("doctortest: no answer for %q", cmdline)
	case a.err != nil:
		return nil, a.err
	case cmd.Done != nil && !cmd.Done(a.res.Stdout):
		return nil, fmt.Errorf("doctortest: %q never gave the answer Done waits for", cmdline)
	}
	res := *a.res
	return &res, nil
}

// Recorded outputs of each probe on a healthy machine: the versions the
// tested ranges start at, signed in everywhere, abridged model lists.
const (
	CodexModels = `{"id":1,"result":{"userAgent":"slop-bot-doctor/0.172.0","codexHome":"/home/u/.codex"}}
{"method":"remoteControl/status/changed","params":{"status":"disabled"}}
{"id":2,"result":{"data":[{"id":"gpt-6.2-sol","model":"gpt-6.2-sol","hidden":false,"isDefault":true},` +
		`{"id":"gpt-6.2-astra","model":"gpt-6.2-astra","hidden":false,"isDefault":false}],"nextCursor":null}}
`
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
// bin, at a tested version, signed in. Repos still need their
// `git -C <path> rev-parse --git-dir` answered.
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
	c.Answer("codex app-server", ok(CodexModels))
	c.Answer("pi --version", ok("1.4.0\n"))
	c.Answer("pi --list-models", ok(PiModels))
	c.Answer("pi auth check --model openai/gpt-6.2-sol --json --no-refresh", ok(PiReady))
	c.Answer("agy --version", ok("1.5.18\n"))
	c.Answer("agy models", ok(AgyModels))
	return c
}

// Calls returns the command lines run so far, in order.
func (c *Commands) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}
