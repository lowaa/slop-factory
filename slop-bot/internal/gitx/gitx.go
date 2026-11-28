// Package gitx runs the git binary for slop-bot: with a context, an
// environment cleaned of anything that would point git at another repo, and
// its stderr carried in the error.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Sentinels for the failures callers branch on. Match them with errors.Is; the
// *Error carrying one still has git's own message.
var (
	ErrGitNotFound     = errors.New("git binary not found")
	ErrNotARepo        = errors.New("not a git repository")
	ErrBranchExists    = errors.New("branch already exists")
	ErrPathExists      = errors.New("path already exists")
	ErrUnknownRevision = errors.New("unknown revision")
	ErrDirtyWorktree   = errors.New("worktree has modified or untracked files")
	ErrNotAWorktree    = errors.New("not a working tree")
	ErrNoSuchSection   = errors.New("no such config section")
)

// stderrKinds maps git's messages (under LC_ALL=C) to sentinels. Order
// matters: the first match wins, and "a branch named ... already exists"
// would otherwise read as ErrPathExists.
var stderrKinds = []struct {
	needle string
	kind   error
}{
	{"not a git repository", ErrNotARepo},
	{"a branch named", ErrBranchExists},
	{"contains modified or untracked files", ErrDirtyWorktree},
	{"is not a working tree", ErrNotAWorktree},
	{"no such section", ErrNoSuchSection},
	{"already exists", ErrPathExists},
	{"unknown revision", ErrUnknownRevision},
	{"invalid reference", ErrUnknownRevision},
	{"bad revision", ErrUnknownRevision},
	{"not a valid object name", ErrUnknownRevision},
	{"needed a single revision", ErrUnknownRevision},
}

// Error is a git command that ran and failed.
type Error struct {
	Args     []string
	Dir      string
	ExitCode int
	Stderr   string
	kind     error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = "no output"
	}
	return fmt.Sprintf("git %s (in %s): exit %d: %s", strings.Join(e.Args, " "), e.Dir, e.ExitCode, msg)
}

// Unwrap returns the sentinel the failure maps to, if any.
func (e *Error) Unwrap() error { return e.kind }

// IsExit reports whether err is a git failure with the given exit code: how
// plumbing such as `symbolic-ref -q` says "no" without it being an error.
func IsExit(err error, code int) bool {
	var ge *Error
	return errors.As(err, &ge) && ge.ExitCode == code
}

// Options configures a Git.
type Options struct {
	// Bin is the git binary: a path, or a name looked up on PATH.
	Bin string
	// Env is the environment git starts from; nil means os.Environ(). Tests
	// pass their own so the user's git config can't leak in.
	Env []string
	// WaitDelay bounds how long Run waits, once git has exited or been told
	// to stop, for its children (hooks, filters) to let go of its output.
	// Zero means DefaultWaitDelay.
	WaitDelay time.Duration
	// Logger defaults to discarding.
	Logger *slog.Logger
}

// DefaultWaitDelay is Options.WaitDelay's default.
const DefaultWaitDelay = 10 * time.Second

// Git runs git commands.
type Git struct {
	bin       string
	env       []string
	waitDelay time.Duration
	logger    *slog.Logger
}

// New resolves the git binary and builds the environment every command runs
// with.
func New(opts *Options) (*Git, error) {
	if opts.Bin == "" {
		return nil, errors.New("gitx: no git binary given")
	}
	bin, err := exec.LookPath(opts.Bin)
	if err != nil {
		return nil, fmt.Errorf("gitx: %w: %s: %w", ErrGitNotFound, opts.Bin, err)
	}
	// os/exec resolves a relative path against each command's Dir, which
	// would run a "git" from inside whatever repo the command is aimed at.
	if bin, err = filepath.Abs(bin); err != nil {
		return nil, fmt.Errorf("gitx: %w", err)
	}
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	g := &Git{bin: bin, env: cleanEnv(env), waitDelay: opts.WaitDelay, logger: opts.Logger}
	if g.waitDelay == 0 {
		g.waitDelay = DefaultWaitDelay
	}
	if g.logger == nil {
		g.logger = slog.New(slog.DiscardHandler)
	}
	return g, nil
}

// repoEnv is what git itself calls its "local env vars" (`git rev-parse
// --local-env-vars`), plus config injected through the environment. Inherited
// from a caller running inside a git hook, any of them would aim our command
// at the caller's repo instead of the one we name.
var repoEnv = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_CONFIG":                       true,
	"GIT_CONFIG_COUNT":                 true,
	"GIT_CONFIG_PARAMETERS":            true,
	"GIT_DIR":                          true,
	"GIT_GRAFT_FILE":                   true,
	"GIT_IMPLICIT_WORK_TREE":           true,
	"GIT_INDEX_FILE":                   true,
	"GIT_INTERNAL_SUPER_PREFIX":        true,
	"GIT_NAMESPACE":                    true,
	"GIT_NO_REPLACE_OBJECTS":           true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_PREFIX":                       true,
	"GIT_REPLACE_REF_BASE":             true,
	"GIT_SHALLOW_FILE":                 true,
	"GIT_WORK_TREE":                    true,
	"GIT_CEILING_DIRECTORIES":          true,
	"GIT_DISCOVERY_ACROSS_FILESYSTEM":  true,
	// Replaced below, so stderr matching sees git's English messages.
	"LC_ALL":   true,
	"LANGUAGE": true,
	// Replaced below: a headless daemon has nobody to answer a prompt.
	"GIT_TERMINAL_PROMPT": true,
}

func cleanEnv(in []string) []string {
	out := make([]string, 0, len(in)+3)
	for _, kv := range in {
		k, _, _ := strings.Cut(kv, "=")
		if repoEnv[k] || strings.HasPrefix(k, "GIT_CONFIG_KEY_") || strings.HasPrefix(k, "GIT_CONFIG_VALUE_") {
			continue
		}
		out = append(out, kv)
	}
	// GIT_OPTIONAL_LOCKS=0 keeps `git status` from taking the index lock in a
	// worktree an agent may be using.
	return append(out, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
}

// Run runs `git args...` in dir and returns its stdout.
func (g *Git) Run(ctx context.Context, dir string, args ...string) (string, error) {
	// #nosec G204 -- bin is the configured git binary; args go to git as argv,
	// never through a shell.
	cmd := exec.CommandContext(ctx, g.bin, args...)
	cmd.Dir = dir
	cmd.Env = g.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// SIGTERM to git's whole process group, not SIGKILL to git alone: git
	// cleans up on SIGTERM (an interrupted `worktree add` removes its half-made
	// worktree instead of leaving it locked "initializing"), and its hooks and
	// filters stop too rather than holding the output pipes open.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return terminateGroup(cmd) }
	cmd.WaitDelay = g.waitDelay
	g.logger.Debug("git", "dir", dir, "args", args)
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil {
		// git exited 0 and its output is complete; a background child it
		// started (a hook's `cmd &`) still held the pipes.
		g.logger.Warn("git exited but a child kept its output open", "dir", dir, "args", args)
		err = nil
	}
	if err == nil {
		return stdout.String(), nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", fmt.Errorf("git %s (in %s): %w", strings.Join(args, " "), dir, ctxErr)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		// It never ran: a missing dir, or the binary gone since New.
		return "", fmt.Errorf("git %s (in %s): %w", strings.Join(args, " "), dir, err)
	}
	return "", &Error{
		Args:     args,
		Dir:      dir,
		ExitCode: exitErr.ExitCode(),
		Stderr:   stderr.String(),
		kind:     classify(stderr.String()),
	}
}

func classify(stderr string) error {
	s := strings.ToLower(stderr)
	for _, k := range stderrKinds {
		if strings.Contains(s, k.needle) {
			return k.kind
		}
	}
	return nil
}
