// Command slop-review runs an independent LLM code review of the current
// branch (or --from-commit-hash onwards) and prints the result as JSON on
// stdout. See review.sh for the intended entry point.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-review/internal/centralconfig"
	"github.com/lowaa/slop-factory/slop-review/internal/config"
	"github.com/lowaa/slop-factory/slop-review/internal/fingerprint"
	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
	"github.com/lowaa/slop-factory/slop-review/internal/orchestrator"
	"github.com/lowaa/slop-factory/slop-review/internal/persona"
	"github.com/lowaa/slop-factory/slop-review/internal/providers"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
	"github.com/lowaa/slop-factory/slop-review/internal/runreport"
	"github.com/lowaa/slop-factory/slop-review/personas"
	"github.com/lowaa/slop-factory/slop-review/report"
	"github.com/lowaa/slop-factory/slop-review/report/reporter"
)

// buildVersion is set at build time (-ldflags "-X main.buildVersion=<sha>")
// when the binary is shipped without a checkout; --tool-version overrides it.
var buildVersion string

// libraryFor is the persona library a run uses: --persona-dir when given,
// else the one embedded in the binary.
func libraryFor(cfg *config.Config) fs.FS {
	if cfg.PersonaDir != "" {
		return os.DirFS(cfg.PersonaDir)
	}
	return personas.FS
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "slop-review:", err)
		os.Exit(1)
	}
}

// output is the single JSON object printed to stdout: Review is the
// machine-parseable verdict/findings, Markdown is the same result rendered
// for a human/log to read directly, Meta records what was actually
// reviewed and by which model, and Sessions how the review was split up,
// for audit trails.
type output struct {
	Review   review.Review                `json:"review"`
	Markdown string                       `json:"markdown"`
	Meta     meta                         `json:"meta"`
	Sessions []orchestrator.SessionReport `json:"sessions"`
	// Report is set only when --report-to/SLOP_REVIEW_REPORT_URL posted
	// successfully -- the ids the validation step (`slop-review label`)
	// needs to refer back to this run and its findings.
	Report *reportOutput `json:"report,omitempty"`
}

type meta struct {
	Branch   string `json:"branch"`
	BaseRef  string `json:"base_ref"`
	Persona  string `json:"persona"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// ReasoningEffort is empty when the model's default applied.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// ProviderSource says where Provider/Model came from: the central
	// config (ProviderConfigRevision), its saved copy, local settings, or
	// the command line.
	ProviderSource         config.ProviderSource `json:"provider_source"`
	ProviderConfigRevision int64                 `json:"provider_config_revision,omitempty"`
	// FellBack is set when some sessions ran on the fallback provider;
	// each session says which it ran on.
	FellBack *orchestrator.FellBack `json:"fell_back,omitempty"`
}

// reportOutput mirrors report.Accepted for stdout.
type reportOutput struct {
	RunID   string `json:"run_id"`
	URL     string `json:"url"`
	Created bool   `json:"created"`
}

func run(args []string, stdout, stderr io.Writer) error {
	return runWith(args, stdout, stderr, &providers.Factory{})
}

// runWith is run with the provider factory a test can point at a fake pi.
func runWith(args []string, stdout, stderr io.Writer, factory *providers.Factory) error {
	if len(args) > 0 && args[0] == "label" {
		return runLabel(context.Background(), args[1:], stdout, stderr, slog.NewTextHandler(stderr, nil))
	}
	inv := &invocation{startedAt: time.Now(), stderr: stderr, logHandler: slog.NewTextHandler(stderr, nil)}
	inv.host, _ = os.Hostname()
	ctx := context.Background()

	cfg, err := config.LoadWith(ctx, args, &centralconfig.Source{LogHandler: inv.logHandler})
	inv.cfg = cfg
	if cfg != nil {
		for _, note := range cfg.Notes {
			_, _ = fmt.Fprintln(stderr, "slop-review: "+note)
		}
		if cfg.ProviderSource == config.SourceFlags && cfg.ReportURL != "" {
			_, _ = fmt.Fprintln(stderr, "slop-review: --provider/--model/--reasoning-effort given: this run uses them, not the central provider config")
		}
	}
	if err != nil {
		return inv.fail(ctx, err, report.FailureConfig)
	}

	// The CLI providers insist on an absolute repo root, and the default
	// is the cwd.
	if abs, err := filepath.Abs(cfg.RepoRoot); err == nil {
		cfg.RepoRoot = abs
	}
	if cfg.ToolVersion == "" {
		cfg.ToolVersion = buildVersion
	}

	scope, err := gitctx.ResolveScope(cfg.RepoRoot, gitctx.ScopeOptions{
		FromCommitHash: cfg.FromCommitHash,
		AllowSecrets:   cfg.AllowSecrets,
		RemoteName:     cfg.ReportRemote,
	})
	if err != nil {
		return inv.fail(ctx, err, report.FailureConfig)
	}
	inv.scope = &scope

	selected, err := persona.LoadMany(libraryFor(cfg), cfg.Persona)
	if err != nil {
		return inv.fail(ctx, err, report.FailureConfig)
	}
	names := make([]string, len(selected))
	for i, p := range selected {
		names[i] = p.Name
	}
	inv.personas = names
	inv.fingerprint = computeFingerprint(cfg, stderr)

	overviews, fullScope, err := overviewInputs(cfg, &scope)
	if err != nil {
		return inv.fail(ctx, err, report.FailureConfig)
	}

	// The factory made homes and scratch directories for the CLI providers'
	// sessions; they are done with once the orchestrator returns.
	defer func() {
		if err := factory.Close(); err != nil {
			_, _ = fmt.Fprintln(stderr, "slop-review: "+err.Error())
		}
	}()
	routes, err := newRoutes(ctx, cfg, factory, inv.logHandler, stderr)
	if err != nil {
		return inv.fail(ctx, err, runreport.Classify(err, report.FailureConfig))
	}

	orch := orchestrator.New(&orchestrator.Options{
		Primary:            routes.primary,
		Fallback:           routes.fallback,
		Trigger:            routes.trigger,
		StartOnFallback:    routes.startOnFallback,
		MaxTurns:           cfg.MaxTurns,
		ToolOutputCapBytes: cfg.ToolOutputCapBytes,
		ContextBudgetBytes: cfg.ContextBudgetBytes,
		AllowSecrets:       cfg.AllowSecrets,
		InlineDiffBytes:    cfg.MaxDiffBytes,
		ChunkDiffBytes:     cfg.ChunkDiffBytes,
		Parallelism:        cfg.Parallelism,
		SeamPass:           cfg.SeamPass,
		FullScope:          fullScope,
		OverviewPersonas:   overviews,
		OverviewCacheDir:   overviewCacheDir(cfg),
		Intent:             cfg.Intent,
	}, inv.logHandler)

	result, err := orch.Run(ctx, &scope, selected)
	if err != nil {
		if result != nil {
			inv.fellBack = result.FellBack
		}
		return inv.fail(ctx, err, runreport.Classify(err, report.FailureOther))
	}
	finishedAt := time.Now()

	r := result.Review
	if fb := result.FellBack; fb != nil {
		// In the review itself, not only stderr: whoever reads the result
		// should know a different provider reviewed some or all of it.
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s failed (%s), so some or all of this review ran on the fallback provider %s; each session says where it ran", fb.From, fb.Class, fb.To))
		if config.ReadsFilesItself(fb.To) {
			r.Warnings = append(r.Warnings, config.SecretsWarning(fb.To))
		}
	}
	r.Warnings = append(r.Warnings, cfg.Warnings...)
	r.Warnings = append(r.Warnings, scope.Warnings...)
	// sendReport builds its report.Run from result.Review, not r -- keep
	// them in sync so a posted report's warnings match stdout's exactly,
	// scope.Warnings included (e.g. a secret-shaped file let through by
	// --allow-secrets), rather than silently omitting them.
	result.Review.Warnings = r.Warnings

	var reportOut *reportOutput
	if cfg.ReportURL != "" {
		reportOut, r.Warnings = sendReport(ctx, cfg, &scope, result, names, inv.fingerprint, inv.host, inv.startedAt, finishedAt, r.Warnings, inv.logHandler)
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output{
		Review:   r,
		Markdown: review.RenderMarkdown(r),
		Meta: meta{
			Branch:                 scope.Branch,
			BaseRef:                scope.BaseRef,
			Persona:                strings.Join(names, ","),
			Provider:               cfg.Provider,
			Model:                  cfg.Model,
			ReasoningEffort:        cfg.ReasoningEffort,
			ProviderSource:         cfg.ProviderSource,
			ProviderConfigRevision: cfg.ProviderConfigRevision,
			FellBack:               result.FellBack,
		},
		Sessions: result.Sessions,
		Report:   reportOut,
	})
}

// overviewInputs loads the library's overview-stage personas and, when the
// review is scoped to a subdirectory, the whole change they must look at.
func overviewInputs(cfg *config.Config, scope *gitctx.Scope) ([]*persona.Persona, *gitctx.Scope, error) {
	overviews, err := persona.LoadOverviews(libraryFor(cfg))
	if err != nil {
		return nil, nil, err
	}
	if len(overviews) == 0 || scope.ScopePath == "" {
		return overviews, nil, nil
	}
	full, err := gitctx.ResolveScope(scope.RepoRoot, gitctx.ScopeOptions{
		FromCommitHash: cfg.FromCommitHash,
		AllowSecrets:   cfg.AllowSecrets,
		RemoteName:     cfg.ReportRemote,
	})
	if err != nil {
		return nil, nil, err
	}
	return overviews, &full, nil
}

// overviewCacheDir is where overview results are kept; "" (no caching) if
// there is no per-user cache directory, which only costs a rerun.
func overviewCacheDir(cfg *config.Config) string {
	if cfg.OverviewCacheDir != "" {
		return cfg.OverviewCacheDir
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "slop-review", "overview")
}

// invocation is what run knows so far, for reporting a run that fails
// before it has a review to report.
type invocation struct {
	startedAt   time.Time
	cfg         *config.Config
	scope       *gitctx.Scope
	personas    []string
	fingerprint *fingerprint.Result
	host        string
	fellBack    *orchestrator.FellBack
	stderr      io.Writer
	logHandler  slog.Handler
}

// computeFingerprint hashes what this run runs with, when it reports
// anywhere. A reviewer that can't read its own source still reviews: the run
// is reported without a fingerprint, with a note on stderr.
func computeFingerprint(cfg *config.Config, stderr io.Writer) *fingerprint.Result {
	if cfg.ReportURL == "" {
		return nil
	}
	var fp *fingerprint.Result
	var err error
	if cfg.SourceDir == "" && buildVersion != "" {
		fp, err = fingerprint.ComputeBundled(buildVersion, libraryFor(cfg))
	} else {
		fp, err = fingerprint.Compute(cfg.SourceDir, libraryFor(cfg))
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "slop-review: the run is reported without a content fingerprint: %v\n", err)
		return nil
	}
	return fp
}

// fail reports the run as failed with class, when there is somewhere to
// report to, and returns err unchanged: reporting is how the human hears
// about a slop-review that has stopped working (lost logins, a usage limit)
// when the error itself only reaches an agent. As with a completed run,
// reporting trouble never changes the outcome -- it is written to stderr,
// since a failed run prints no JSON to carry it.
func (inv *invocation) fail(ctx context.Context, err error, class report.FailureClass) error {
	if inv.cfg == nil || inv.cfg.ReportURL == "" {
		return err
	}
	failed := runreport.BuildFailed(&runreport.FailedInput{
		Err: err, Class: class, Cfg: inv.cfg, Scope: inv.scope, Personas: inv.personas, Fingerprint: inv.fingerprint,
		Host: inv.host, FellBack: inv.fellBack, StartedAt: inv.startedAt, FinishedAt: time.Now(),
	})
	if warning := deliver(ctx, inv.cfg, failed, inv.logHandler).Warning; warning != "" {
		_, _ = fmt.Fprintln(inv.stderr, "slop-review: "+warning)
	}
	return err
}

// sendReport builds the report.Run for this invocation, retries whatever
// an earlier invocation couldn't deliver, then posts this one. Reporting
// failure of any kind -- including a report that fails Validate, which
// would otherwise be a slop-review bug the server never gets to reject --
// never fails the review: it's folded into warnings instead, and returned
// alongside them since sendReport is the last place with access to both.
func sendReport(ctx context.Context, cfg *config.Config, scope *gitctx.Scope, result *orchestrator.Result, personas []string, fp *fingerprint.Result, host string, startedAt, finishedAt time.Time, warnings []string, logHandler slog.Handler) (*reportOutput, []string) {
	warn := func(format string, args ...any) (*reportOutput, []string) {
		return nil, append(warnings, fmt.Sprintf("run not reported: "+format, args...))
	}

	// A run with no remote URL can never pass report.Run.Validate (see
	// report.RemoteKey) -- caught here, before building or spooling
	// anything, so the warning names the actual fix (--report-remote or
	// --report-repo) instead of Validate's more generic complaint, and so
	// a run that can never be valid is never spooled to be retried forever.
	if cfg.ReportRepo == "" && scope.RemoteURL == "" {
		return warn("no git remote identifies this repo; set --report-remote or --report-repo")
	}

	reportRun := runreport.Build(runreport.Input{
		Result: result, Scope: scope, Cfg: cfg, Personas: personas, Fingerprint: fp,
		HeadSHA: scope.HeadSHA, Dirty: scope.Dirty, Host: host, StartedAt: startedAt, FinishedAt: finishedAt,
	})
	sendResult := deliver(ctx, cfg, reportRun, logHandler)
	if sendResult.Warning != "" {
		warnings = append(warnings, sendResult.Warning)
	}
	if sendResult.Accepted == nil {
		return nil, warnings
	}
	return &reportOutput{
		RunID:   sendResult.Accepted.RunID.String(),
		URL:     sendResult.Accepted.URL,
		Created: sendResult.Accepted.Created,
	}, warnings
}

// routes is what the orchestrator runs sessions on.
type routes struct {
	primary         orchestrator.Route
	fallback        *orchestrator.Route
	trigger         func(error) string
	startOnFallback string
}

// buildRoute makes everything the orchestrator needs to run sessions on
// route: the provider for the built-in loop, or the session factory for a
// provider that runs its own (codex, claude).
func buildRoute(ctx context.Context, cfg *config.Config, factory *providers.Factory, route *config.Route, logHandler slog.Handler) (orchestrator.Route, error) {
	out := orchestrator.Route{Name: route.Provider, Model: route.Model, BillingMode: route.BillingMode}
	sessions, err := factory.Sessions(ctx, route, &providers.RunOptions{RepoRoot: cfg.RepoRoot, RunChecks: cfg.RunChecks}, logHandler)
	if err != nil {
		return out, err
	}
	if sessions != nil {
		out.Sessions = sessions
		return out, nil
	}
	out.Provider, err = factory.New(ctx, route)
	return out, err
}

// newRoutes builds the primary provider and, when the central config names
// one this machine can use, the fallback. A primary that can't even start
// -- its login check failing, say -- goes straight to the fallback when
// that failure is one the config falls back on; otherwise it fails the
// run, as it always has.
func newRoutes(ctx context.Context, cfg *config.Config, factory *providers.Factory, logHandler slog.Handler, stderr io.Writer) (*routes, error) {
	primary, primaryErr := buildRoute(ctx, cfg, factory, &cfg.Route, logHandler)
	rs := &routes{primary: primary}

	if cfg.Fallback != nil {
		fallback, err := buildRoute(ctx, cfg, factory, cfg.Fallback, logHandler)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "slop-review: the fallback provider %q can't be used on this machine, so this run has none: %v\n", cfg.Fallback.Provider, err)
		} else {
			rs.fallback = &fallback
			rs.trigger = func(err error) string {
				if class := runreport.Classify(err, ""); slices.Contains(cfg.FallbackOn, class) {
					return string(class)
				}
				return ""
			}
		}
	}
	if primaryErr != nil {
		if rs.trigger == nil || rs.trigger(primaryErr) == "" {
			return nil, primaryErr
		}
		rs.startOnFallback = rs.trigger(primaryErr)
		_, _ = fmt.Fprintf(stderr, "slop-review: %s can't start (%v); every session runs on the fallback %s\n", cfg.Provider, primaryErr, cfg.Fallback.Provider)
	}
	return rs, nil
}

// deliver validates run, retries whatever an earlier invocation couldn't
// deliver, then posts run. Every failure comes back as the result's
// warning, never an error.
func deliver(ctx context.Context, cfg *config.Config, run *report.Run, logHandler slog.Handler) reporter.Result {
	if err := run.Validate(); err != nil {
		return reporter.Result{Warning: fmt.Sprintf("run not reported: built an invalid report: %v", err)}
	}

	tokens, err := cfg.ReportTokenSource()
	if err != nil {
		return reporter.Result{Warning: fmt.Sprintf("run not reported: %v", err)}
	}
	if tokens == nil {
		tokens = reporter.StaticToken("")
	}
	rep, err := reporter.New(cfg.ReportURL, "", cfg.ReportSpoolDir, logHandler, reporter.WithTokenSource(tokens))
	if err != nil {
		return reporter.Result{Warning: fmt.Sprintf("run not reported: %v", err)}
	}

	// Give whatever an earlier invocation couldn't deliver another chance
	// before adding this run to the pile.
	rep.RetrySpooled(ctx)
	return rep.Send(ctx, run)
}
