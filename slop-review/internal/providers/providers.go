// Package providers builds the llm.LLMProvider a Config selects, so the
// choice of backend lives in one place instead of in each command.
package providers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lowaa/slop-factory/slop-review/internal/claudecli"
	"github.com/lowaa/slop-factory/slop-review/internal/codexcli"
	"github.com/lowaa/slop-factory/slop-review/internal/config"
	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/piauth"
	"github.com/lowaa/slop-factory/slop-review/internal/session"
)

// codexSessionTimeout bounds one `codex exec`. Codex has no turn limit
// slop-review can set, so this is what stops a wedged run holding the
// review forever; it is generous because a large part legitimately takes
// a while.
const codexSessionTimeout = 30 * time.Minute

// claudeSessionTimeout bounds one `claude -p`, for the same reason.
const claudeSessionTimeout = 30 * time.Minute

// goEnvTimeout bounds `go env GOCACHE` and `git rev-parse`, local lookups
// that only stall if the toolchain is mid-download or the disk is wedged.
const goEnvTimeout = 10 * time.Second

// RunOptions is what a CLI provider's sandbox depends on beyond its route.
type RunOptions struct {
	// RepoRoot is the repository under review.
	RepoRoot string
	// RunChecks lets the reviewing CLI build and run tests (see
	// config.Config.RunChecks).
	RunChecks bool
}

// Factory builds providers. The zero value is ready to use.
type Factory struct {
	// PiCommand overrides the pi executable the chatgpt provider gets its
	// token from (see piauth.Options.Command); tests point it at a fake.
	PiCommand []string
	// BaseURLs overrides where an OpenAI-compatible provider is reached,
	// by provider name; tests point a fallback at a fake with it, which
	// config can't do (a local base URL only ever applies to the provider
	// it was set for).
	BaseURLs map[string]string
	// CodexCommand and CodexHome override the codex executable and the
	// isolated CODEX_HOME the codex provider uses; tests point them at a
	// fake and a temp dir.
	CodexCommand string
	CodexHome    string
	// ClaudeCommand overrides the claude executable the claude provider
	// runs, and ClaudeSandboxTools what its sandbox needs; tests point them
	// at fakes.
	ClaudeCommand      string
	ClaudeSandboxTools []string

	// tempHomes are the per-run CODEX_HOMEs this factory made, and
	// scratchDirs the per-run check scratch directories; Close removes
	// both. Guarded by mu: routes are built one after another today, but
	// the factory is shared.
	mu          sync.Mutex
	tempHomes   []string
	scratchDirs []string
}

// Close removes anything the factory created for its sessions. Call it
// once every session it built has finished.
func (f *Factory) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var errs []error
	for _, dir := range f.tempHomes {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, fmt.Errorf("providers: remove codex home: %w", err))
		}
	}
	f.tempHomes = nil
	for _, dir := range f.scratchDirs {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, fmt.Errorf("providers: remove check scratch dir: %w", err))
		}
	}
	f.scratchDirs = nil
	return errors.Join(errs...)
}

// Sessions returns the session factory a CLI route (codex, claude) reviews
// with, after checking its login and sandbox prerequisites. Any other route
// returns nil: its sessions are the built-in loop over New's provider.
func (f *Factory) Sessions(ctx context.Context, route *config.Route, run *RunOptions, logHandler slog.Handler) (session.Factory, error) {
	switch route.ProviderKind {
	case config.ProviderKindCodex:
		return f.codexSessions(ctx, route, run, logHandler)
	case config.ProviderKindClaude:
		return f.claudeSessions(ctx, route, run, logHandler)
	default:
		return nil, nil
	}
}

func (f *Factory) codexSessions(ctx context.Context, route *config.Route, run *RunOptions, logHandler slog.Handler) (session.Factory, error) {
	dir := f.CodexHome
	if dir == "" {
		// Fresh per run rather than a stable path: Codex writes its own
		// state into CODEX_HOME (memories, goals, session databases), so a
		// reused home would carry one review's leftovers into the next,
		// and two runs at once would share it.
		tmp, err := os.MkdirTemp("", "slop-review-codex-home-")
		if err != nil {
			return nil, fmt.Errorf("providers: codex home: %w", err)
		}
		f.track(&f.tempHomes, tmp)
		dir = tmp
	}
	home, err := codexcli.PrepareIsolatedHome(dir)
	if err != nil {
		return nil, err
	}
	runner := codexcli.New(&codexcli.Options{
		Command:       f.CodexCommand,
		Home:          home,
		Timeout:       codexSessionTimeout,
		RunChecks:     run.RunChecks,
		WritableRoots: f.writableRoots(ctx, run, logHandler),
		TempDir:       f.checkTempDir(ctx, run, logHandler),
	}, logHandler)
	if err := runner.CheckLogin(ctx); err != nil {
		// Wrapped as an auth failure so a configured fallback can take it.
		return nil, fmt.Errorf("%w: %w; %s", llm.ErrAuth, err, codexcli.LoginHint)
	}
	return &session.CodexCLIFactory{Exec: runner, Model: route.Model, ReasoningEffort: route.ReasoningEffort, RunChecks: run.RunChecks}, nil
}

func (f *Factory) claudeSessions(ctx context.Context, route *config.Route, run *RunOptions, logHandler slog.Handler) (session.Factory, error) {
	runner := claudecli.New(&claudecli.Options{
		Command:       f.ClaudeCommand,
		SandboxTools:  f.ClaudeSandboxTools,
		Timeout:       claudeSessionTimeout,
		RunChecks:     run.RunChecks,
		WritableRoots: f.writableRoots(ctx, run, logHandler),
		TempDir:       f.checkTempDir(ctx, run, logHandler),
	}, logHandler)
	// Each failure means this machine can't use claude, so each is an auth
	// failure: the provider is chosen centrally, and a configured fallback
	// should take a machine that lacks it rather than fail the run.
	if err := runner.CheckLogin(ctx); err != nil {
		if errors.Is(err, claudecli.ErrNotLoggedIn) {
			return nil, fmt.Errorf("%w: %w; %s", llm.ErrAuth, err, claudecli.LoginHint)
		}
		return nil, fmt.Errorf("%w: %w", llm.ErrAuth, err)
	}
	if err := runner.CheckSandbox(); err != nil {
		return nil, fmt.Errorf("%w: %w", llm.ErrAuth, err)
	}
	return &session.ClaudeCLIFactory{Exec: runner, Model: route.Model, ReasoningEffort: route.ReasoningEffort, RunChecks: run.RunChecks}, nil
}

func (f *Factory) track(list *[]string, dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*list = append(*list, dir)
}

// checkTempDir makes the one scratch directory a run-checks shell gets as
// its TMPDIR, instead of the system temp dir: a repository can itself live
// under the system temp dir (bench builds its cases there), and granting
// that would make it writable. Empty means no scratch dir, so checks run
// without a writable temp dir.
func (f *Factory) checkTempDir(ctx context.Context, run *RunOptions, logHandler slog.Handler) string {
	if !run.RunChecks {
		return ""
	}
	// Fail closed, as for the build cache.
	ctx, cancel := context.WithTimeout(ctx, goEnvTimeout)
	defer cancel()
	top, err := exec.CommandContext(ctx, "git", "-C", run.RepoRoot, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	repoTop := strings.TrimSpace(string(top))
	logger := slog.New(logHandler)
	// The parent is checked before anything is created in it: a TMPDIR
	// inside the repository would otherwise put the scratch dir in the
	// checkout before it could be refused. A temp dir that merely contains
	// the repository is fine -- the scratch dir lands beside it, not in it.
	parent, perr := canonicalPath(os.TempDir())
	repo, rerr := canonicalPath(repoTop)
	if perr != nil || rerr != nil || within(repo, parent) {
		logger.Warn("not creating a run-checks temp dir inside the repository", "tmpdir", os.TempDir(), "repo", repoTop)
		return ""
	}
	dir, err := os.MkdirTemp("", "slop-review-checks-")
	if err != nil {
		return ""
	}
	f.track(&f.scratchDirs, dir)
	// The canonical path is what the sandbox must be given: on macOS the
	// temp dir is a symlink.
	safe := safeWritableRoots([]string{dir}, repoTop, logger)
	if len(safe) == 0 {
		return ""
	}
	return safe[0]
}

// writableRoots are the directories a run-checks sandbox must let the
// shell write beyond its scratch temp dir: today only Go's build cache, without
// which `go build` cannot even start. A missing or failing go just means
// no hole is punched; the run itself must not depend on it.
func (f *Factory) writableRoots(ctx context.Context, run *RunOptions, logHandler slog.Handler) []string {
	if !run.RunChecks {
		return nil
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, goEnvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, goBin, "env", "GOCACHE").Output()
	if err != nil {
		return nil
	}
	dir := strings.TrimSpace(string(out))
	// GOCACHE=off disables the cache and is not a path.
	if !filepath.IsAbs(dir) {
		return nil
	}
	// Fail closed: without the toplevel we cannot tell whether the root
	// would unprotect the checkout, so checks just run without a cache.
	top, err := exec.CommandContext(ctx, "git", "-C", run.RepoRoot, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil
	}
	return safeWritableRoots([]string{dir}, strings.TrimSpace(string(top)), slog.New(logHandler))
}

// safeWritableRoots drops every candidate that would make the reviewed
// checkout writable: the filesystem root, the repository, an ancestor of it,
// or anything inside it. GOCACHE is environment-controlled, so it must not
// be able to unprotect the read-only repository.
func safeWritableRoots(candidates []string, repoTop string, log *slog.Logger) []string {
	if !filepath.IsAbs(repoTop) {
		return nil
	}
	repo, err := canonicalPath(repoTop)
	if err != nil {
		return nil
	}
	var kept []string
	for _, c := range candidates {
		dir, err := canonicalPath(c)
		if err != nil || dir == "/" || within(repo, dir) || within(dir, repo) {
			log.Warn("not granting a writable root that overlaps the repository", "root", c)
			continue
		}
		kept = append(kept, dir)
	}
	return kept
}

// within reports whether path is dir or inside it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// canonicalPath resolves symlinks, using the nearest existing parent for the
// part that does not exist yet (the cache dir is created on first use).
func canonicalPath(p string) (string, error) {
	p = filepath.Clean(p)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		missing = append(missing, filepath.Base(p))
		p = parent
	}
}

// New returns the provider route.ProviderKind names, after checking that
// whatever it depends on outside this process is in place. route must come
// from config.Load, which has already resolved and validated the profile.
func (f *Factory) New(ctx context.Context, route *config.Route) (llm.LLMProvider, error) {
	switch route.ProviderKind {
	case config.ProviderKindOpenAICompatible:
		baseURL := route.BaseURL
		if u, ok := f.BaseURLs[route.Provider]; ok {
			baseURL = u
		}
		return llm.NewOpenAIAPILLMProvider(&llm.OpenAIOptions{BaseURL: baseURL, APIKey: route.APIKey, ReportsCost: route.ReportsCost}), nil
	case config.ProviderKindChatGPT:
		tokens := piauth.New(&piauth.Options{Command: f.PiCommand})
		// Checked here, not in review.sh, so every way of running
		// slop-review (bench.sh runs the binary directly) fails the same
		// way before any session starts.
		if err := tokens.Check(ctx); err != nil {
			return nil, fmt.Errorf("%w: %v; %s", llm.ErrAuth, err, llm.LoginHint)
		}
		return llm.NewResponsesLLMProvider(&llm.ResponsesOptions{
			BaseURL:         route.BaseURL,
			Tokens:          tokens,
			ReasoningEffort: route.ReasoningEffort,
			MaxRetries:      2,
			RetryDelay:      2 * time.Second,
		}), nil
	default:
		return nil, fmt.Errorf("providers: no provider for kind %q (provider %q)", route.ProviderKind, route.Provider)
	}
}
