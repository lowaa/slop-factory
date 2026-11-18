// Package codexcli runs one prompt through `codex exec` and returns its
// structured final message. Codex runs its own agent loop and tools; this
// package only decides what it may do (read-only, no network, no skills)
// and what shape it must answer in.
package codexcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrNotLoggedIn is returned by CheckLogin when Codex has no credentials.
var ErrNotLoggedIn = errors.New("codexcli: codex is not logged in")

// LoginHint says how to fix ErrNotLoggedIn.
const LoginHint = "run `codex login` (the login is read from $CODEX_HOME, default ~/.codex)"

// permissionProfile names the profile every run is held to.
const permissionProfile = "slop-review"

type Options struct {
	// Command is the codex executable; tests point it at a fake. Defaults
	// to "codex".
	Command string
	// Home is the isolated CODEX_HOME runs use (see PrepareIsolatedHome).
	// Empty uses whatever CODEX_HOME the process inherited.
	Home string
	// Timeout bounds one Exec. Zero means only ctx bounds it.
	Timeout time.Duration
	// RunChecks lets the shell build and run tests: the repository stays
	// read-only and the network stays off, but WritableRoots and TempDir
	// become writable.
	RunChecks bool
	// WritableRoots are the extra directories (build caches) writable
	// under RunChecks. Ignored without it.
	WritableRoots []string
	// TempDir is a scratch directory made for this run, writable under
	// RunChecks and exported as TMPDIR. It stands in for the system temp
	// dir, which could contain the repository. Ignored without RunChecks.
	TempDir string
}

type Runner struct {
	command       string
	home          string
	timeout       time.Duration
	runChecks     bool
	writableRoots []string
	tempDir       string
	logger        *slog.Logger
}

func New(opts *Options, logHandler slog.Handler) *Runner {
	command := opts.Command
	if command == "" {
		command = "codex"
	}
	return &Runner{
		command:       command,
		home:          opts.Home,
		timeout:       opts.Timeout,
		runChecks:     opts.RunChecks,
		writableRoots: opts.WritableRoots,
		tempDir:       opts.TempDir,
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

// CheckLogin fails unless `codex login status` reports a login in the
// runner's home, so a missing login fails the run before any session
// starts rather than once per session.
func (r *Runner) CheckLogin(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, r.command, "login", "status")
	cmd.Env = r.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotLoggedIn, strings.TrimSpace(string(out)))
	}
	return nil
}

type ExecRequest struct {
	// Cwd is the directory Codex treats as its workspace.
	Cwd    string
	Prompt string
	// Schema is the JSON Schema the final message must match; see
	// StrictSchema for the form Codex accepts.
	Schema []byte
	// Model and ReasoningEffort are left to Codex's own config when empty.
	Model           string
	ReasoningEffort string
}

type ExecResult struct {
	// JSON is the final message, constrained by the request's Schema.
	JSON []byte
	// Model, Effort and Version are what Codex's banner says it actually
	// used -- a config profile or a provider-side default can make that
	// differ from what was asked for.
	Model   string
	Effort  string
	Version string
	// TokensUsed is the total Codex's transcript ends with; zero when it
	// printed none.
	TokensUsed int
}

// Exec runs req to completion. The run's files (prompt, schema, transcript)
// are removed on success and kept on failure, because a failed run's
// transcript is the only way to tell "it could not check X" from "it never
// tried".
func (r *Runner) Exec(ctx context.Context, req *ExecRequest) (*ExecResult, error) {
	return r.run(ctx, req.Cwd, req.Prompt, func(dir, outPath string) ([]string, error) {
		schemaPath := filepath.Join(dir, "schema.json")
		if err := os.WriteFile(schemaPath, req.Schema, 0o600); err != nil {
			return nil, fmt.Errorf("codexcli: write schema: %w", err)
		}
		return r.args(req, schemaPath, outPath), nil
	})
}

// run is the plumbing every kind of codex invocation shares: a scratch dir,
// the process group kill, the kept transcript, and the banner and token
// parsing. buildArgs gets the scratch dir and where the final message must
// be written.
func (r *Runner) run(ctx context.Context, cwd, stdin string, buildArgs func(dir, outPath string) ([]string, error)) (*ExecResult, error) {
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	dir, err := os.MkdirTemp("", "slop-review-codex-*")
	if err != nil {
		return nil, fmt.Errorf("codexcli: scratch dir: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			r.logger.Warn("codex run failed; its files were kept", "dir", dir)
			return
		}
		_ = os.RemoveAll(dir)
	}()

	outPath := filepath.Join(dir, "final.json")
	args, err := buildArgs(dir, outPath)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, r.command, args...)
	cmd.Dir = cwd
	cmd.Env = r.env()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var transcript bytes.Buffer
	cmd.Stdout = &transcript
	cmd.Stderr = &transcript
	// Codex runs its tools as child processes; killing only codex on
	// cancellation would orphan whatever command it was in the middle of.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second

	runErr := cmd.Run()
	// Written before anything can return, since the runs worth reading
	// are the ones that failed.
	_ = os.WriteFile(filepath.Join(dir, "codex.log"), transcript.Bytes(), 0o600)

	blob, readErr := os.ReadFile(outPath)
	switch {
	case runErr != nil:
		return nil, fmt.Errorf("codexcli: codex exec failed: %w: %s", runErr, tail(transcript.String(), 2000))
	case readErr != nil:
		return nil, fmt.Errorf("codexcli: codex produced no final message: %w: %s", readErr, tail(transcript.String(), 2000))
	}

	failed = false
	model, effort, version := bannerFields(transcript.String())
	return &ExecResult{JSON: blob, Model: model, Effort: effort, Version: version, TokensUsed: tokensUsed(transcript.String())}, nil
}

type ReviewRequest struct {
	// Cwd is the repository Codex reviews.
	Cwd string
	// Base is the revision whose `git diff` Codex reviews, uncommitted
	// edits included.
	Base            string
	Model           string
	ReasoningEffort string
}

// Review runs Codex's own built-in review (`codex exec review`) and returns
// its final message in ExecResult.JSON, which is prose rather than JSON.
// No custom instructions are passed: the point is Codex's default review.
func (r *Runner) Review(ctx context.Context, req *ReviewRequest) (*ExecResult, error) {
	if err := validateBase(req.Base); err != nil {
		return nil, err
	}
	return r.run(ctx, req.Cwd, "", func(_, outPath string) ([]string, error) {
		return r.reviewArgs(req, outPath), nil
	})
}

// validateBase refuses a base that codex would read as a flag. It is passed
// as its own argv entry, so this is defence in depth, like gitctx's own
// argument checks.
func validateBase(base string) error {
	if base == "" || strings.HasPrefix(base, "-") {
		return fmt.Errorf("codexcli: review base %q must be a revision, not empty or a flag", base)
	}
	return nil
}

// reviewArgs builds the `codex exec review` command line. `exec review`
// takes no -C or --color: the working directory is the process's, and the
// transcript is plain when it is not a terminal.
func (r *Runner) reviewArgs(req *ReviewRequest, outPath string) []string {
	a := []string{"exec", "review", "--base", req.Base}
	a = append(a, r.isolationFlags()...)
	a = append(a, "-o", outPath)
	return append(a, r.modelFlags(req.Model, req.ReasoningEffort)...)
}

// args builds the `codex exec` command line.
//
// The sandbox is a permission profile set through -c, never -s/--sandbox:
// a `default_permissions` profile beats `sandbox_mode`, so a profile set
// anywhere else would silently override a plain sandbox mode.
//
// The sandbox only governs Codex's shell. Everything else that could reach
// outside it -- MCP servers from the user's config, ChatGPT apps, plugins,
// web search -- is switched off, and so is the skill catalogue, which
// Codex reinstalls into CODEX_HOME on every start and would otherwise
// offer the model a review rubric of its own. Checked against codex 0.154
// by asking a run to list its tools and skills: what is left is the shell.
func (r *Runner) args(req *ExecRequest, schemaPath, outPath string) []string {
	a := []string{"exec", "--color", "never"}
	a = append(a, r.isolationFlags()...)
	a = append(a,
		"-C", req.Cwd,
		"--output-schema", schemaPath,
		"-o", outPath,
	)
	a = append(a, r.modelFlags(req.Model, req.ReasoningEffort)...)
	// The prompt goes on stdin: it carries a whole diff, which can exceed
	// the OS's argument length limit.
	return append(a, "-")
}

// isolationFlags are what every kind of run is held to, whether it is a
// persona review or Codex's own: see args for why each is there.
func (r *Runner) isolationFlags() []string {
	a := []string{
		// Nothing a review run writes under CODEX_HOME is worth keeping.
		"--ephemeral",
		// Login still comes from CODEX_HOME; only config.toml is skipped.
		"--ignore-user-config",
		"-c", "skills.include_instructions=false",
		"--disable", "apps",
		"--disable", "plugins",
		"--disable", "image_generation",
		"-c", `web_search="disabled"`,
		"-c", `approval_policy="never"`,
	}
	return append(a, sandboxFlags(r.runChecks, r.writeRoots())...)
}

// modelFlags leaves the model and effort to Codex's own config when empty.
func (r *Runner) modelFlags(model, effort string) []string {
	var a []string
	if model != "" {
		a = append(a, "-m", model)
	}
	if effort != "" {
		a = append(a, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	}
	return a
}

// sandboxFlags are the -c overrides that hold a run to the read-only,
// offline profile, shared with `codex sandbox` so the e2e test exercises
// the exact policy a review gets.
//
// With runChecks the profile also writes to writableRoots (the run's
// scratch temp dir and build caches), because without them even `go build`
// fails creating its work dir. Not Codex's ":tmpdir": that is the system
// temp dir, which can contain the repository. The filesystem override must
// be the inline-table form: the dotted and list forms do not parse.
func sandboxFlags(runChecks bool, writableRoots []string) []string {
	flags := []string{
		"-c", fmt.Sprintf(`permissions.%s.extends=":read-only"`, permissionProfile),
		"-c", fmt.Sprintf(`permissions.%s.network.enabled=false`, permissionProfile),
	}
	if runChecks {
		entries := make([]string, 0, len(writableRoots))
		for _, root := range writableRoots {
			if q, ok := tomlString(root); ok {
				entries = append(entries, q+`="write"`)
			}
		}
		if len(entries) > 0 {
			flags = append(flags, "-c", fmt.Sprintf("permissions.%s.filesystem={%s}", permissionProfile, strings.Join(entries, ",")))
		}
	}
	return append(flags, "-c", fmt.Sprintf(`default_permissions=%q`, permissionProfile))
}

// tomlString quotes s as a TOML basic string. A path with control
// characters is refused rather than escaped: it is not a cache dir worth
// a sandbox hole.
func tomlString(s string) (string, bool) {
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, true
}

// writeRoots are the roots the run-checks profile makes writable.
func (r *Runner) writeRoots() []string {
	roots := slices.Clone(r.writableRoots)
	if r.tempDir != "" {
		roots = append(roots, r.tempDir)
	}
	return roots
}

func (r *Runner) env() []string {
	env := os.Environ()
	if r.runChecks && r.tempDir != "" {
		// Appended after the inherited value so it wins; Go and mktemp
		// both follow it.
		env = append(env, "TMPDIR="+r.tempDir)
	}
	if r.runChecks {
		// The repository is read-only, so a Python check would fail
		// trying to write __pycache__ into it.
		env = append(env, "PYTHONDONTWRITEBYTECODE=1")
	}
	if r.home != "" {
		// Appended last so it wins over an inherited CODEX_HOME.
		env = append(env, "CODEX_HOME="+r.home)
	}
	return env
}

var bannerRe = regexp.MustCompile(`(?m)^(?:OpenAI Codex v(\S+)|model: (\S+)|reasoning effort: (\S+))\s*$`)

// bannerFields reads the header `codex exec` prints before it starts:
//
//	OpenAI Codex v0.154.0
//	--------
//	workdir: /repo
//	model: gpt-5.6-luna
//	reasoning effort: medium
func bannerFields(transcript string) (model, effort, version string) {
	for _, m := range bannerRe.FindAllStringSubmatch(transcript, -1) {
		switch {
		case m[1] != "" && version == "":
			version = m[1]
		case m[2] != "" && model == "":
			model = m[2]
		case m[3] != "" && effort == "":
			effort = m[3]
		}
	}
	return model, effort, version
}

var tokensUsedRe = regexp.MustCompile(`(?m)^tokens used\s*\n\s*([\d,]+)\s*$`)

// tokensUsed reads the count `codex exec` ends its transcript with:
//
//	tokens used
//	11,720
//
// The last one wins; 0 when there is none, since a missing count must not
// fail an otherwise good review.
func tokensUsed(transcript string) int {
	ms := tokensUsedRe.FindAllStringSubmatch(transcript, -1)
	if len(ms) == 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.ReplaceAll(ms[len(ms)-1][1], ",", ""))
	if err != nil {
		return 0
	}
	return n
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
