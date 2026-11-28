// Package orchestrator runs a review of a change set as one or more
// sessions: each selected persona over each chunk of the change, in
// parallel, then a pass over the seams between chunks, then a merge of
// everything into one review. A small change under one persona is a single
// session, exactly as if nothing were orchestrating it.
package orchestrator

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"

	"github.com/lowaa/slop-factory/slop-review/internal/chunk"
	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/persona"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
	"github.com/lowaa/slop-factory/slop-review/internal/session"
	"github.com/lowaa/slop-factory/slop-review/internal/toolset"
)

// Route is one provider sessions can run on.
type Route struct {
	// Name is the provider's name, as reported (e.g. "chatgpt").
	Name     string
	Provider llm.LLMProvider
	Model    string
	// BillingMode is how the provider bills, reported with each session's
	// usage.
	BillingMode llmusage.BillingMode
	// Sessions, if set, makes the route's sessions. Nil means the managed
	// loop over Provider and Model.
	Sessions session.Factory
}

type Options struct {
	// Primary is where every session starts.
	Primary Route
	// Fallback, if set, is where a session goes when the primary fails it
	// in a way Trigger accepts -- retried once, whole -- and where every
	// session not yet started goes from then on, so a run doesn't keep
	// hitting a primary that is out of quota. Sessions that already
	// succeeded on the primary keep their results.
	Fallback *Route
	// Trigger names the failure class of a primary's error if it takes the
	// fallback, and returns "" if it doesn't.
	Trigger func(err error) string
	// StartOnFallback, when set, is the class the primary already failed
	// with before any session started (its login check, say): every
	// session runs on the fallback.
	StartOnFallback string

	MaxTurns           int
	ToolOutputCapBytes int
	ContextBudgetBytes int
	AllowSecrets       bool

	// InlineDiffBytes bounds how much diff one session's prompt carries;
	// ChunkDiffBytes is the size the planner aims each chunk at. Keeping
	// the chunk no bigger than the inline budget means a chunked session
	// normally sees all of its files up front.
	InlineDiffBytes int
	ChunkDiffBytes  int

	// Parallelism caps concurrent sessions -- the provider's rate limit,
	// not local resources, is what this protects.
	Parallelism int
	// SeamPass enables the cross-chunk session run after a split review.
	SeamPass bool

	// FullScope is the whole change, for the overview stage: a review
	// pointed at a subdirectory still needs its overview to see every
	// dependent of that change. Nil means the scope being reviewed is
	// already the whole change.
	FullScope *gitctx.Scope
	// OverviewPersonas are the overview-stage personas every review wants
	// the result of, whichever personas were asked for. Ones also asked for
	// by name run fresh; the rest are reused from OverviewCacheDir when it
	// holds a result for this diff, and run otherwise.
	OverviewPersonas []*persona.Persona
	// OverviewCacheDir holds overview results, keyed by the diff. Empty
	// disables the cache.
	OverviewCacheDir string

	// Intent is the author's statement of what the change is meant to do
	// (typically its PR description), given to every session beside the
	// commit log. Empty means none.
	Intent string
}

// SessionReport is the audit record of one session: what it covered and
// how it went, whether or not its findings survived the merge.
type SessionReport struct {
	Persona  string         `json:"persona"`
	Part     int            `json:"part"`
	Parts    int            `json:"parts"`
	Files    []string       `json:"files"`
	Verdict  review.Verdict `json:"verdict,omitempty"`
	Summary  string         `json:"summary,omitempty"`
	Findings int            `json:"findings"`
	// Incomplete and Error distinguish a session that ran out of turns but
	// reported something from one that produced nothing at all.
	Incomplete bool   `json:"incomplete,omitempty"`
	Error      string `json:"error,omitempty"`
	// Provider and Model are what the session ran on (last, when it was
	// retried on the fallback).
	Provider string `json:"provider"`
	Model    string `json:"model"`

	// Telemetry, gathered whether or not the session finished cleanly (see
	// session.Session.Stats). Field names match report.Session so
	// internal/runreport can map this straight across; the token counts
	// are in go-llm-usage's terms (see llm.Usage).
	Turns              int                  `json:"turns"`
	Elisions           int                  `json:"elisions"`
	UnseenFiles        []string             `json:"unseen_files,omitempty"`
	BillingMode        llmusage.BillingMode `json:"billing_mode,omitempty"`
	Requests           int64                `json:"requests"`
	InputTokens        int64                `json:"input_tokens"`
	CacheReadTokens    int64                `json:"cache_read_tokens"`
	OutputTokens       int64                `json:"output_tokens"`
	ReasoningTokens    int64                `json:"reasoning_tokens"`
	ReportedCostMicros *int64               `json:"reported_cost_microdollars,omitempty"`
	Tools              []llmusage.ToolUsage `json:"tools,omitempty"`
	DurationMilli      int64                `json:"duration_ms"`
}

type Result struct {
	Review   review.Review
	Sessions []SessionReport
	// FellBack is set when any session ran on the fallback.
	FellBack *FellBack
}

// FellBack says a run moved to its fallback provider, and why.
type FellBack struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Class is the primary's failure that triggered it.
	Class string `json:"class"`
}

// routing is one run's choice of route: the primary until a session fails
// with a trigger class, the fallback from then on. Shared by the run's
// concurrent sessions.
type routing struct {
	opts *Options
	mu   sync.Mutex
	// class is the trigger that moved the run to the fallback; "" while
	// it's on the primary.
	class string
}

func newRouting(opts *Options) *routing {
	rt := &routing{opts: opts}
	if opts.Fallback != nil {
		rt.class = opts.StartOnFallback
	}
	return rt
}

// current is the route a session starting now runs on.
func (rt *routing) current() *Route {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.class != "" {
		return rt.opts.Fallback
	}
	return &rt.opts.Primary
}

// shouldRetry reports whether a session that failed on route with err
// is retried on the fallback, and moves the run there if so. The first
// trigger names the move; later ones only confirm it.
func (rt *routing) shouldRetry(route *Route, err error) bool {
	if rt.opts.Fallback == nil || route == rt.opts.Fallback || rt.opts.Trigger == nil {
		return false
	}
	class := rt.opts.Trigger(err)
	if class == "" {
		return false
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.class == "" {
		rt.class = class
	}
	return true
}

func (rt *routing) fellBack() *FellBack {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.class == "" {
		return nil
	}
	return &FellBack{From: rt.opts.Primary.Name, To: rt.opts.Fallback.Name, Class: rt.class}
}

type Orchestrator struct {
	opts   *Options
	logger *slog.Logger
}

func New(opts *Options, logHandler slog.Handler) *Orchestrator {
	return &Orchestrator{opts: opts, logger: slog.New(logHandler)}
}

//go:embed guidance.md
var guidance string

// withGuidance appends the guidance every session shares to its persona's
// prompt. It is added where the session is built, not in each persona, so
// the seam pass and the overview stage get it too.
func withGuidance(systemPrompt string) string {
	return systemPrompt + "\n\n" + guidance
}

// SeamsPersona names the cross-chunk pass in reports and attributions.
const SeamsPersona = "seams"

// job is one session to run.
type job struct {
	persona *persona.Persona
	part    part
	// overview is the overview stage's result, for the session's prompt.
	overview string
}

// outcome is what one job produced.
type outcome struct {
	review review.Review
	err    error
	// stats is gathered whether or not the session ended in err, so a
	// failed session's turns/tokens spent still show up in the report.
	stats session.Stats
	// route is what the session ran on; nil if it never started.
	route *Route
}

// Run reviews scope under every persona and merges the result. When every
// session failed, the error comes with a Result that has no review but
// still says which sessions ran where, and whether the run fell back.
func (o *Orchestrator) Run(ctx context.Context, scope *gitctx.Scope, personas []*persona.Persona) (*Result, error) {
	rt := newRouting(o.opts)

	var jobs []job
	var outcomes []outcome
	overviewText := ""
	if explicit, rest := splitStages(personas); len(explicit)+len(o.opts.OverviewPersonas) > 0 {
		full := o.opts.FullScope
		if full == nil {
			full = scope
		}
		var implicit []*persona.Persona
		for _, p := range o.opts.OverviewPersonas {
			if !slices.ContainsFunc(explicit, func(e *persona.Persona) bool { return e.Name == p.Name }) {
				implicit = append(implicit, p)
			}
		}
		stage := o.runOverview(ctx, rt, full, explicit, implicit)
		jobs, outcomes, overviewText = stage.jobs, stage.outcomes, stage.text
		personas = rest
	}
	reviewStart := len(jobs)

	split := false
	for _, p := range personas {
		chunks := o.plan(scope, p)
		if len(chunks) > 1 {
			split = true
		}
		for i, c := range chunks {
			jobs = append(jobs, job{persona: p, part: part{index: i + 1, of: len(chunks), owned: c.Files}, overview: overviewText})
		}
	}
	o.logger.Info("review planned", "personas", len(personas), "sessions", len(jobs), "files", len(scope.Files))

	outcomes = append(outcomes, o.runAll(ctx, rt, scope, jobs[reviewStart:])...)

	if split && o.opts.SeamPass {
		seamJob, seamOutcome := o.runSeamPass(ctx, rt, scope, jobs, outcomes)
		jobs = append(jobs, seamJob)
		outcomes = append(outcomes, seamOutcome)
	}

	result, err := merge(scope, jobs, outcomes)
	if result != nil {
		result.FellBack = rt.fellBack()
	}
	return result, err
}

func (o *Orchestrator) plan(scope *gitctx.Scope, p *persona.Persona) []chunk.Chunk {
	if p.Chunking == persona.ChunkWhole {
		return chunk.Whole(scope.Files)
	}
	return chunk.ByPackage(scope.Files, o.opts.ChunkDiffBytes)
}

// runAll runs jobs with at most Parallelism in flight, returning outcomes
// in job order. Each session gets its own toolset, since a Dispatcher
// tracks per-session state (what it has seen, what it warned about); the
// provider is shared and safe for concurrent use.
func (o *Orchestrator) runAll(ctx context.Context, rt *routing, scope *gitctx.Scope, jobs []job) []outcome {
	outcomes := make([]outcome, len(jobs))
	sem := make(chan struct{}, max(o.opts.Parallelism, 1))
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				outcomes[i] = outcome{err: ctx.Err()}
				return
			}
			defer func() { <-sem }()
			outcomes[i] = o.runJob(ctx, rt, scope, &jobs[i])
		}(i)
	}
	wg.Wait()
	return outcomes
}

func (o *Orchestrator) runJob(ctx context.Context, rt *routing, scope *gitctx.Scope, j *job) outcome {
	prompt := buildSessionPrompt(scope, o.opts.Intent, &j.part, j.overview, o.opts.InlineDiffBytes)
	return o.runSession(ctx, rt, scope, j, j.persona.Prompt, prompt.text, prompt.inlined)
}

// runSession runs one session on the run's current route, and once more on
// the fallback if the primary fails it with a trigger class. The retry
// starts the session over, whole: a conversation carries provider state
// (encrypted reasoning, say) that another provider can't read.
func (o *Orchestrator) runSession(ctx context.Context, rt *routing, scope *gitctx.Scope, j *job, systemPrompt, userPrompt string, inlined []string) outcome {
	route := rt.current()
	out := o.runSessionOn(ctx, route, scope, j, systemPrompt, userPrompt, inlined)
	if out.err != nil && ctx.Err() == nil && rt.shouldRetry(route, out.err) {
		o.logger.Warn("session failed on the primary provider; retrying on the fallback",
			"persona", j.persona.Name, "part", j.part.index, "from", route.Name, "to", o.opts.Fallback.Name, "err", out.err)
		out = o.runSessionOn(ctx, o.opts.Fallback, scope, j, systemPrompt, userPrompt, inlined)
	}
	return out
}

func (o *Orchestrator) runSessionOn(ctx context.Context, route *Route, scope *gitctx.Scope, j *job, systemPrompt, userPrompt string, inlined []string) outcome {
	tools := toolset.New(&toolset.Options{
		// scope.RepoRoot, not wherever the review was pointed: the
		// toolset has to be rooted at the repository toplevel the diff's
		// paths are relative to (see gitctx.Scope).
		RepoRoot:       scope.RepoRoot,
		OutputCapBytes: o.opts.ToolOutputCapBytes,
		AllowSecrets:   o.opts.AllowSecrets,
		Files:          scope.Files,
		Owned:          j.part.owned,
	})
	tools.MarkSeen(inlined...)

	factory := route.Sessions
	if factory == nil {
		factory = &session.SlopReviewManagedFactory{
			Provider:           route.Provider,
			Model:              route.Model,
			MaxTurns:           o.opts.MaxTurns,
			ContextBudgetBytes: o.opts.ContextBudgetBytes,
		}
	}
	sess := factory.New(&session.Spec{
		RepoRoot:     scope.RepoRoot,
		SystemPrompt: withGuidance(systemPrompt),
		UserPrompt:   userPrompt,
		Tools:        tools,
	})
	r, err := sess.Run(ctx)
	stats := sess.Stats()
	o.logger.Info("session finished",
		"persona", j.persona.Name, "part", j.part.index, "of", j.part.of, "provider", route.Name,
		"findings", len(r.Findings), "incomplete", r.Incomplete, "err", err,
		"turns", stats.Turns, "elisions", stats.Elisions)
	return outcome{review: r, err: err, stats: stats, route: route}
}

// merge folds every session's outcome into one review. It fails only if
// no session produced anything: a partial review with its gaps named is
// worth more than an error, but a review of nothing is not a review. A
// failed merge still returns the sessions, for reporting the failure.
func merge(scope *gitctx.Scope, jobs []job, outcomes []outcome) (*Result, error) {
	result := &Result{Sessions: make([]SessionReport, len(jobs))}
	merged := &result.Review
	merged.Verdict = review.VerdictApprove
	var findings []review.Finding
	var summaries []string
	var errs []error
	succeeded := 0

	for i := range jobs {
		j, out := &jobs[i], &outcomes[i]
		label := sessionLabel(j)
		u := &out.stats.Usage
		report := SessionReport{
			Persona: j.persona.Name, Part: j.part.index, Parts: j.part.of, Files: j.part.owned,
			Turns: out.stats.Turns, Elisions: out.stats.Elisions, UnseenFiles: out.stats.UnseenFiles,
			Requests: u.Requests, InputTokens: u.InputTokens, CacheReadTokens: u.CacheReadTokens,
			OutputTokens: u.OutputTokens, ReasoningTokens: u.ReasoningTokens, ReportedCostMicros: u.ReportedCostMicros,
			Tools:         out.stats.Tools,
			DurationMilli: out.stats.Duration.Milliseconds(),
		}
		if out.route != nil {
			report.Provider, report.Model, report.BillingMode = out.route.Name, out.route.Model, out.route.BillingMode
		}

		if out.err != nil {
			report.Error = out.err.Error()
			result.Sessions[i] = report
			errs = append(errs, fmt.Errorf("%s: %w", label, out.err))
			merged.Incomplete = true
			merged.Warnings = append(merged.Warnings, fmt.Sprintf("%s was not reviewed: %v", label, out.err))
			continue
		}
		succeeded++

		r := &out.review
		report.Verdict, report.Summary, report.Findings, report.Incomplete = r.Verdict, r.Summary, len(r.Findings), r.Incomplete
		result.Sessions[i] = report

		merged.Verdict = review.WorseVerdict(merged.Verdict, r.Verdict)
		merged.Incomplete = merged.Incomplete || r.Incomplete
		for _, w := range r.Warnings {
			merged.Warnings = append(merged.Warnings, label+": "+w)
		}
		for _, f := range r.Findings {
			f.Personas = []string{j.persona.Name}
			findings = append(findings, f)
		}
		summaries = append(summaries, fmt.Sprintf("%s: %s", label, r.Summary))
	}

	if succeeded == 0 {
		return result, fmt.Errorf("orchestrator: every review session failed: %w", errors.Join(errs...))
	}

	checked, warnings := checkFindings(scope, findings)
	merged.Warnings = append(merged.Warnings, warnings...)
	merged.Findings = dedupe(checked)
	merged.Verdict = review.WorseVerdict(merged.Verdict, review.VerdictFor(merged.Findings))
	if merged.Incomplete {
		merged.Verdict = review.WorseVerdict(merged.Verdict, review.VerdictIncomplete)
	}

	if len(jobs) == 1 {
		merged.Summary = outcomes[0].review.Summary
	} else {
		merged.Summary = fmt.Sprintf("Merged from %d review sessions.\n\n- %s", len(jobs), strings.Join(summaries, "\n- "))
	}
	return result, nil
}

func sessionLabel(j *job) string {
	if j.part.of <= 1 {
		return j.persona.Name
	}
	return fmt.Sprintf("%s (part %d of %d)", j.persona.Name, j.part.index, j.part.of)
}
