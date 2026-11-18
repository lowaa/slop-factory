// Package claudecli runs one prompt through `claude -p
// --dangerously-skip-permissions` and returns its structured result.
// Claude Code runs its own agent loop and tools; this package only decides
// what it may do and what shape it must answer in.
//
// Skipping permissions is what lets a non-interactive run use its shell at
// all, and it means nothing asks before a command runs. What holds the run
// read-only and offline instead is Claude Code's own sandbox, configured
// here so it can't be argued out of: the shell can write only to the run's
// own scratch directory (and, with RunChecks, the build caches and a scratch
// temp dir), all network is denied, and the dangerouslyDisableSandbox
// escape hatch is off. The sandbox only governs the shell; the file tools
// that could write (Edit, Write, ...) are simply not offered. Secret-shaped
// files and credential stores are denied to the shell and the file tools
// alike (see readDenied).
package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
)

// ErrNotLoggedIn is returned by CheckLogin when Claude Code has no
// credentials.
var ErrNotLoggedIn = errors.New("claudecli: claude is not logged in")

// ErrUnavailable is returned when the claude executable can't be run at all.
var ErrUnavailable = errors.New("claudecli: claude is not installed or not runnable")

// ErrUsageLimit is returned when Claude Code reports the plan's usage
// limit, which is what a configured fallback is for.
var ErrUsageLimit = errors.New("claudecli: claude hit its usage limit")

// ErrNoSandbox is returned when Claude Code could not start its sandbox
// (e.g. bubblewrap is missing). The run is refused rather than made
// unsandboxed.
var ErrNoSandbox = errors.New("claudecli: claude's sandbox is unavailable (is bubblewrap installed?)")

// LoginHint says how to fix ErrNotLoggedIn.
const LoginHint = "run `claude auth login` (the login is read from $CLAUDE_CONFIG_DIR, default ~/.claude)"

// tools is everything a review session is offered: reading and a shell.
const tools = "Read,Grep,Glob,Bash"

// strippedEnv are variables a Claude Code session sets for its children.
// slop-review is usually run by an agent, so without this the review would
// inherit that agent's session identity and its channel for messaging it.
var strippedEnv = []string{
	"CLAUDECODE",
	"CLAUDE_PID",
	"CLAUDE_EFFORT",
	"CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_EXECPATH",
	"CLAUDE_CODE_SESSION_ID",
	"CLAUDE_CODE_CHILD_SESSION",
	"CLAUDE_CODE_SESSION_ATTENDED",
	"CLAUDE_CODE_MESSAGING_SOCKET",
	"CLAUDE_CODE_MESSAGING_TOKEN",
	// A slop-haus slot sets its own model and effort this way. Inherited,
	// they would pick the review's model and effort whenever the central
	// config leaves them to the provider's default.
	"ANTHROPIC_MODEL",
	"CLAUDE_CODE_EFFORT_LEVEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL_FORCE",
}

type Options struct {
	// Command is the claude executable; tests point it at a fake. Defaults
	// to "claude".
	Command string
	// Timeout bounds one Exec. Zero means only ctx bounds it.
	Timeout time.Duration
	// RunChecks lets the shell build and run tests: the repository stays
	// read-only and the network stays off, but WritableRoots become
	// writable.
	RunChecks bool
	// WritableRoots are the extra directories (build caches) writable
	// under RunChecks. Ignored without it.
	WritableRoots []string
	// TempDir is a scratch directory made for this run, writable under
	// RunChecks and exported as TMPDIR. It stands in for the system temp
	// dir, which could contain the repository. Ignored without RunChecks.
	TempDir string
	// SandboxTools are the executables Claude Code's sandbox needs on PATH;
	// nil means this platform's (bubblewrap and socat on Linux). Tests
	// point it at ones that don't exist.
	SandboxTools []string
}

type Runner struct {
	command       string
	timeout       time.Duration
	runChecks     bool
	writableRoots []string
	tempDir       string
	sandboxTools  []string
	logger        *slog.Logger
}

func New(opts *Options, logHandler slog.Handler) *Runner {
	command := opts.Command
	if command == "" {
		command = "claude"
	}
	return &Runner{
		command:       command,
		timeout:       opts.Timeout,
		runChecks:     opts.RunChecks,
		writableRoots: slices.Clone(opts.WritableRoots),
		tempDir:       opts.TempDir,
		sandboxTools:  sandboxTools(opts.SandboxTools),
		logger:        slog.New(logHandler),
	}
}

// Sandbox reports the run-checks settings the runner was built with, so
// callers wiring it up can verify them.
func (r *Runner) Sandbox() (runChecks bool, writableRoots []string) {
	return r.runChecks, r.writableRoots
}

// TempDir reports the scratch temp dir the runner was built with.
func (r *Runner) TempDir() string { return r.tempDir }

// CheckLogin fails unless `claude auth status` reports a login, so a
// missing login fails the run before any session starts rather than once
// per session.
func (r *Runner) CheckLogin(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, r.command, "auth", "status")
	cmd.Env = r.env()
	out, err := cmd.Output()
	var status struct {
		LoggedIn bool `json:"loggedIn"`
	}
	// The exit code alone isn't trusted: what says "logged in" is the
	// status itself.
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		// It never ran (missing, not executable, context ended): not
		// evidence about the login.
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err != nil || json.Unmarshal(out, &status) != nil || !status.LoggedIn {
		return fmt.Errorf("%w: %s", ErrNotLoggedIn, strings.TrimSpace(string(out)))
	}
	return nil
}

// sandboxTools is what the sandbox needs on this platform: on Linux it is
// built from bubblewrap, with socat bridging its network filter. macOS's
// sandbox-exec ships with the OS.
func sandboxTools(override []string) []string {
	if override != nil {
		return slices.Clone(override)
	}
	if runtime.GOOS == "linux" {
		return []string{"bwrap", "socat"}
	}
	return nil
}

// sandboxPackages maps a sandbox tool to the Debian/Ubuntu package that
// provides it, where the two names differ.
var sandboxPackages = map[string]string{"bwrap": "bubblewrap"}

func installHint(tool string) string {
	pkg := tool
	if p, ok := sandboxPackages[tool]; ok {
		pkg = p
	}
	return "install it with: apt install " + pkg
}

// CheckSandbox fails when a tool Claude Code's sandbox is built from is
// missing. Claude Code would refuse every session for it (failIfUnavailable),
// so it is better found once, before any starts.
func (r *Runner) CheckSandbox() error {
	for _, tool := range r.sandboxTools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%w: %s is not on PATH (%s); a service's PATH comes from its own environment, not ~/.bashrc or the login shell's",
				ErrNoSandbox, tool, installHint(tool))
		}
	}
	return nil
}

type ExecRequest struct {
	// RepoRoot is the repository under review. The run is started in a
	// scratch directory of its own with RepoRoot added to it, because
	// Claude Code writes bookkeeping (.claude/) into its working
	// directory, which would show up as an untracked file in the very
	// diff the next review reads.
	RepoRoot string
	Prompt   string
	// Schema is the JSON Schema the result must match.
	Schema []byte
	// Model and ReasoningEffort are left to Claude Code's defaults when
	// empty.
	Model           string
	ReasoningEffort string
}

type ExecResult struct {
	// JSON is the structured result, validated against the request's
	// Schema by Claude Code.
	JSON []byte
	// Models are the models the run reports it used: an alias or a
	// provider-side default can make that differ from what was asked for.
	Models []string
	// Tokens is input + output + cache reads + cache writes across every
	// model the run used. Cache reads are included because they still draw
	// on a subscription's usage, just at a discount.
	Tokens int
}

// result is the part of `--output-format json` this package reads.
type result struct {
	IsError          bool                       `json:"is_error"`
	Subtype          string                     `json:"subtype"`
	Result           string                     `json:"result"`
	StructuredOutput json.RawMessage            `json:"structured_output"`
	ModelUsage       map[string]json.RawMessage `json:"modelUsage"`
	Usage            tokenUsage                 `json:"usage"`
}

// tokenUsage is the run's `usage` block.
type tokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u tokenUsage) total() int {
	return u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// modelTokens is one model's share of `modelUsage`, which (unlike `usage`)
// also covers any subagents' models.
type modelTokens struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
}

// Exec runs req to completion. The run's scratch directory (its working
// directory and transcript) is removed on success and kept on failure,
// because a failed run's output is the only way to tell what went wrong.
func (r *Runner) Exec(ctx context.Context, req *ExecRequest) (*ExecResult, error) {
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	// A terminal ^C or a supervisor's SIGTERM reaches us as well as the
	// process group; cancelling ctx kills claude's whole group via
	// cmd.Cancel instead of leaving its tool commands running.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dir, err := os.MkdirTemp("", "slop-review-claude-*")
	if err != nil {
		return nil, fmt.Errorf("claudecli: scratch dir: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			r.logger.Warn("claude run failed; its files were kept", "dir", dir)
			return
		}
		_ = os.RemoveAll(dir)
	}()
	// dir comes from the inherited TMPDIR; if that is inside the repo, the
	// run's one writable directory would be too.
	if err := checkWritableRoot("scratch dir", dir, req.RepoRoot); err != nil {
		return nil, err
	}
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		return nil, fmt.Errorf("claudecli: work dir: %w", err)
	}

	stdout, stderr, runErr := r.run(ctx, req, work)
	// Written before anything can return, since the runs worth reading
	// are the ones that failed.
	_ = os.WriteFile(filepath.Join(dir, "claude.json"), stdout, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "claude.stderr"), stderr, 0o600)

	res, err := decodeResult(stdout, stderr, runErr)
	if err != nil {
		return nil, err
	}
	res.JSON = redactLogin(res.JSON)
	failed = false
	return res, nil
}

// run runs claude for req with work as its working directory, the only
// place its sandbox lets the shell write.
func (r *Runner) run(ctx context.Context, req *ExecRequest, work string) (stdout, stderr []byte, err error) {
	// The sandbox's deny list is built from RepoRoot, and claude resolves
	// a relative one against work, which would leave the real repository
	// writable.
	if !filepath.IsAbs(req.RepoRoot) {
		return nil, nil, fmt.Errorf("claudecli: repo root must be absolute, got %q", req.RepoRoot)
	}
	a, err := r.args(req)
	if err != nil {
		return nil, nil, err
	}
	if _, err := exec.LookPath(r.command); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	cmd := exec.CommandContext(ctx, r.command, a...)
	cmd.Dir = work
	cmd.Env = r.env()
	cmd.Stdin = strings.NewReader(req.Prompt)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	// Claude Code runs its tools as child processes; killing only claude
	// on cancellation would orphan whatever command it was in the middle
	// of.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second

	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// decodeResult reads what a run printed into its result, or the reason it
// has none.
func decodeResult(stdout, stderr []byte, runErr error) (*ExecResult, error) {
	var res result
	decodeErr := json.Unmarshal(stdout, &res)
	switch {
	case runErr != nil && sandboxFailure(string(stderr)+string(stdout)):
		return nil, fmt.Errorf("%w: %s", ErrNoSandbox, tail(string(stderr)+string(stdout), 500))
	case runErr != nil:
		return nil, fmt.Errorf("claudecli: claude failed: %w: %s", runErr, tail(string(stderr)+string(stdout), 2000))
	case decodeErr != nil:
		return nil, fmt.Errorf("claudecli: claude printed no result: %w: %s", decodeErr, tail(string(stdout), 2000))
	case res.IsError && usageLimit(&res):
		return nil, fmt.Errorf("%w: %s", ErrUsageLimit, tail(res.Result, 500))
	case res.IsError:
		return nil, fmt.Errorf("claudecli: claude reported an error (%s): %s", res.Subtype, tail(res.Result, 2000))
	case len(res.StructuredOutput) == 0 || string(res.StructuredOutput) == "null":
		return nil, fmt.Errorf("claudecli: claude produced no structured output (%s): %s", res.Subtype, tail(res.Result, 2000))
	}

	models := make([]string, 0, len(res.ModelUsage))
	for m := range res.ModelUsage {
		models = append(models, m)
	}
	slices.Sort(models)

	tokens := 0
	for _, raw := range res.ModelUsage {
		var m modelTokens
		if json.Unmarshal(raw, &m) == nil {
			tokens += m.InputTokens + m.OutputTokens + m.CacheReadInputTokens + m.CacheCreationInputTokens
		}
	}
	if tokens == 0 {
		tokens = res.Usage.total()
	}
	return &ExecResult{JSON: res.StructuredOutput, Models: models, Tokens: tokens}, nil
}

// args builds the `claude -p` command line.
//
// --setting-sources "" keeps out the user's and the project's settings,
// CLAUDE.md, hooks and plugins, all of which would otherwise sit alongside
// the persona as instructions slop-review never chose -- a global
// CLAUDE.md that says "run slop-review before you're done" among them.
// MCP servers and slash commands are switched off for the same reason.
// Checked against Claude Code 2.1.309 by asking a run to list its tools
// and quote its instructions, and to write and connect out with its shell.
func (r *Runner) args(req *ExecRequest) ([]string, error) {
	var allowWrite []string
	if r.runChecks {
		allowWrite = slices.Clone(r.writableRoots)
		if r.tempDir != "" {
			allowWrite = append(allowWrite, r.tempDir)
		}
		for _, w := range allowWrite {
			if err := checkWritableRoot("writable root", w, req.RepoRoot); err != nil {
				return nil, err
			}
		}
	}
	home, _ := os.UserHomeDir()
	logins, err := loginDirs(req.RepoRoot, os.Getenv)
	if err != nil {
		return nil, err
	}
	settings, err := sandboxSettings(req.RepoRoot, allowWrite, readDenied(req.RepoRoot, home, os.Getenv("CLAUDE_CONFIG_DIR"), logins))
	if err != nil {
		return nil, err
	}
	a := []string{
		"-p",
		"--dangerously-skip-permissions",
		"--output-format", "json",
		"--json-schema", string(req.Schema),
		"--tools", tools,
		"--add-dir", req.RepoRoot,
		"--setting-sources", "",
		"--settings", settings,
		"--strict-mcp-config",
		"--disable-slash-commands",
		"--no-session-persistence",
	}
	if req.Model != "" {
		a = append(a, "--model", req.Model)
	}
	if req.ReasoningEffort != "" {
		a = append(a, "--effort", req.ReasoningEffort)
	}
	// The prompt goes on stdin: it carries a whole diff, which can exceed
	// the OS's argument length limit.
	return a, nil
}

// sandboxSettings is the --settings JSON every run is held to.
//
// The sandbox already lets the shell write only to its working directory
// and the --add-dir directories, so the repository is the one write to take
// back. Not "/": Claude Code keeps its own per-command bookkeeping under
// the system temp dir, and denying that makes every shell result end in an
// "Operation not permitted" the model then has to reason about. An empty
// allowedDomains on its own denies nothing, hence deniedDomains.
//
// allowWrite (build caches, under RunChecks) is what lets `go build` work;
// it is added alongside denyWrite, which still covers the repository.
//
// denyRead (secret-shaped files and credential stores, see readDenied) is
// held twice: by the sandbox for the shell, and by permission rules for the
// file tools, which the sandbox doesn't govern. Both still hold with
// permissions skipped (checked against Claude Code 2.1.319).
func sandboxSettings(repoRoot string, allowWrite, denyRead []string) (string, error) {
	filesystem := map[string]any{"denyWrite": []string{repoRoot}}
	if len(allowWrite) > 0 {
		filesystem["allowWrite"] = allowWrite
	}
	settings := map[string]any{"autoMemoryEnabled": false}
	if len(denyRead) > 0 {
		filesystem["denyRead"] = denyRead
		settings["permissions"] = map[string]any{"deny": readDenyRules(denyRead)}
	}
	settings["sandbox"] = map[string]any{
		"enabled":                  true,
		"failIfUnavailable":        true,
		"allowUnsandboxedCommands": false,
		"filesystem":               filesystem,
		"network": map[string]any{
			"allowedDomains": []string{},
			"deniedDomains":  []string{"*"},
		},
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("claudecli: settings: %w", err)
	}
	return string(b), nil
}

// loginEnv are the credential-shaped variables claude itself signs in with,
// so they survive secretShaped.
var loginEnv = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}

// secretShapedParts mark a variable as a credential, as Codex's default
// shell_environment_policy does. Callers load the repo's .env before
// running slop-review, so without this the review's shell would hold the
// report token and every API key there, and a diff that talks the model
// into quoting `env` would carry them out in a finding.
var secretShapedParts = []string{"KEY", "SECRET", "TOKEN", "PASSWORD", "PASSWD", "CREDENTIAL"}

func secretShaped(name string) bool {
	if slices.Contains(loginEnv, name) {
		return false
	}
	upper := strings.ToUpper(name)
	// GIT_CONFIG_KEY_<n> names a git setting, not a credential; dropping it
	// while GIT_CONFIG_COUNT stays makes every git command fail.
	if strings.HasPrefix(upper, "GIT_CONFIG_KEY_") {
		return false
	}
	if strings.HasPrefix(upper, "SLOP_REVIEW_") {
		return true
	}
	return slices.ContainsFunc(secretShapedParts, func(p string) bool { return strings.Contains(upper, p) })
}

func (r *Runner) env() []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(strippedEnv, name) || secretShaped(name)
	})
	// Belt and braces with autoMemoryEnabled: a review has nothing to
	// remember, and memory lives outside the sandbox's reach.
	env = append(env, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
	if r.runChecks && r.tempDir != "" {
		env = append(env, "TMPDIR="+r.tempDir)
	}
	if r.runChecks {
		// The repository is read-only, so a Python check would fail
		// trying to write __pycache__ into it.
		env = append(env, "PYTHONDONTWRITEBYTECODE=1")
	}
	return env
}

// usageLimitPhrases are Claude Code's wordings of a plan's limit ("Claude
// AI usage limit reached", "You've hit your weekly limit", "5-hour limit
// reached"). A context or recursion limit is something else.
var usageLimitPhrases = []string{"usage limit", "hit your", "-hour limit", "weekly limit"}

// usageLimit reports whether an error result is the plan's limit. Only an
// API-level error counts: one of the error_* subtypes (max turns, an error
// during execution) carries the model's own text, which could be about
// rate limits in the code under review. The limit message is short; a
// long result is the model talking.
func usageLimit(res *result) bool {
	if strings.HasPrefix(res.Subtype, "error_") || len(res.Result) > 300 {
		return false
	}
	msg := strings.ToLower(res.Result)
	return strings.Contains(msg, "limit") && slices.ContainsFunc(usageLimitPhrases, func(p string) bool { return strings.Contains(msg, p) })
}

// sandboxFailure spots Claude Code refusing to start because
// failIfUnavailable found no sandbox.
func sandboxFailure(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "sandbox") && (strings.Contains(l, "unavailable") || strings.Contains(l, "bubblewrap") || strings.Contains(l, "bwrap"))
}

// checkWritableRoot refuses a writable directory that would expose the
// repository: one that is not absolute, is "/", or is the repo, an
// ancestor of it, or inside it.
func checkWritableRoot(kind, root, repo string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("claudecli: %s %q must be absolute", kind, root)
	}
	root, repo = resolve(root), resolve(repo)
	if root == string(filepath.Separator) || within(repo, root) || within(root, repo) {
		return fmt.Errorf("claudecli: %s %q would make the repository %q writable", kind, root, repo)
	}
	return nil
}

// within reports whether path is dir or inside it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolve follows symlinks as far as the path exists, so a link can't
// disguise the repo.
func resolve(p string) string {
	p = filepath.Clean(p)
	var rest []string
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{r}, rest...)...)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
