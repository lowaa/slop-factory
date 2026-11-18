package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/persona"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
	"github.com/lowaa/slop-factory/slop-review/internal/session"
)

// baseSystem is a session's persona prompt without the guidance every
// session gets appended.
func baseSystem(system string) string {
	return strings.TrimSuffix(system, "\n\n"+guidance)
}

// fakeProvider answers every session's first turn with a submit_review,
// built by respond from that session's prompts. It is safe for concurrent
// use, like the real provider, and records every request it saw.
type fakeProvider struct {
	respond func(system, user string) (review.Review, error)

	mu       sync.Mutex
	requests []llm.CompletionRequest
	inFlight int
	maxSeen  int
	// overlap, if set, holds each call until this many are in flight (or
	// a timeout passes), so a parallelism test can't pass merely because
	// calls happened to finish before the next began; hold then keeps it
	// in flight a little longer, long enough for an unbounded pool to
	// start every other call.
	overlap int
	hold    time.Duration
}

func (p *fakeProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.inFlight++
	p.maxSeen = max(p.maxSeen, p.inFlight)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}()

	if p.overlap > 0 {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			p.mu.Lock()
			reached := p.maxSeen >= p.overlap
			p.mu.Unlock()
			if reached {
				break
			}
			time.Sleep(time.Millisecond)
		}
		time.Sleep(p.hold)
	}

	r, err := p.respond(baseSystem(req.Messages[0].Content), req.Messages[1].Content)
	if err != nil {
		return llm.CompletionResponse{}, err
	}
	args, err := json.Marshal(r)
	if err != nil {
		return llm.CompletionResponse{}, err
	}
	return llm.CompletionResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "submit", Name: review.SubmitReviewToolName, ArgumentsJSON: string(args)},
	}}}, nil
}

// fakeProviderWithUsage wraps fakeProvider to attach fixed token usage to
// every response, for tests that check telemetry propagation rather than
// the request/response content fakeProvider itself is built around.
type fakeProviderWithUsage struct {
	*fakeProvider
	usage llm.Usage
}

func (p *fakeProviderWithUsage) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	resp, err := p.fakeProvider.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	resp.Usage = p.usage
	return resp, nil
}

func (p *fakeProvider) userPrompts(system string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, r := range p.requests {
		if baseSystem(r.Messages[0].Content) == system {
			out = append(out, r.Messages[1].Content)
		}
	}
	return out
}

var yoursRe = regexp.MustCompile(`(?m)^- \[yours\] (\S+)`)

// ownedFiles reads back which files a session was told are its own.
func ownedFiles(userPrompt string) []string {
	var out []string
	for _, m := range yoursRe.FindAllStringSubmatch(userPrompt, -1) {
		out = append(out, m[1])
	}
	return out
}

// approve is a respond func that finds nothing.
func approve(string, string) (review.Review, error) {
	return review.Review{Verdict: review.VerdictApprove, Summary: "fine"}, nil
}

// fixture is a scope over a real directory, so checkFindings has files to
// check against. Each file's diff is size bytes of added lines.
func fixture(t *testing.T, sizes map[string]int) *gitctx.Scope {
	t.Helper()
	root := t.TempDir()
	scope := &gitctx.Scope{Branch: "feature", BaseRef: "base", RepoRoot: root}
	for _, path := range sortedKeys(sizes) {
		full := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(strings.Repeat("line\n", 10)), 0o644))
		header := fmt.Sprintf("diff --git a/%s b/%s\n@@ -0,0 +1 @@\n", path, path)
		body := "+" + strings.Repeat("x", max(sizes[path]-len(header)-2, 0)) + "\n"
		scope.Files = append(scope.Files, gitctx.FileDiff{Path: path, Added: 1, Block: header + body})
	}
	return scope
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func newOrchestrator(provider llm.LLMProvider, opts Options) *Orchestrator {
	opts.Primary = Route{Name: "primary", Provider: provider, Model: "m", BillingMode: llmusage.BillingMetered}
	opts.MaxTurns = 3
	opts.ToolOutputCapBytes = 4096
	if opts.InlineDiffBytes == 0 {
		opts.InlineDiffBytes = 100000
	}
	if opts.ChunkDiffBytes == 0 {
		opts.ChunkDiffBytes = 100000
	}
	if opts.Parallelism == 0 {
		opts.Parallelism = 4
	}
	return New(&opts, slog.DiscardHandler)
}

var (
	standard = &persona.Persona{Name: "standard", Prompt: "STANDARD", Chunking: persona.ChunkByPackage}
	security = &persona.Persona{Name: "security", Prompt: "SECURITY", Chunking: persona.ChunkWhole}
)

func TestRun_SmallChangeIsOneSession(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200, "b/two.go": 200})
	provider := &fakeProvider{respond: func(string, string) (review.Review, error) {
		return review.Review{Verdict: review.VerdictApprove, Summary: "the one summary"}, nil
	}}

	result, err := newOrchestrator(provider, Options{SeamPass: true}).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	require.Len(t, provider.requests, 1, "no split, so no seam pass either")
	prompt := provider.requests[0].Messages[1].Content
	assert.NotContains(t, prompt, "split into")
	assert.Contains(t, prompt, "+xxx", "the diff is inlined")
	assert.ElementsMatch(t, []string{"a/one.go", "b/two.go"}, ownedFiles(prompt))
	assert.Equal(t, "the one summary", result.Review.Summary)
	assert.Equal(t, review.VerdictApprove, result.Review.Verdict)
	require.Len(t, result.Sessions, 1)
	assert.Equal(t, 1, result.Sessions[0].Parts)
}

// TestRun_SessionReportCarriesTelemetry checks that a session's stats
// (turns used, token usage) reach the merged Result's SessionReport --
// internal/runreport builds the posted report straight from these fields,
// so a wiring mistake here would silently ship zeros for every run.
func TestRun_SessionReportCarriesTelemetry(t *testing.T) {
	cost := int64(1234)
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProviderWithUsage{
		fakeProvider: &fakeProvider{respond: func(string, string) (review.Review, error) {
			return review.Review{Verdict: review.VerdictApprove, Summary: "fine"}, nil
		}},
		usage: llm.Usage{Requests: 2, InputTokens: 500, CacheReadTokens: 400, OutputTokens: 75, ReasoningTokens: 30, ReportedCostMicros: &cost},
	}

	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	require.Len(t, result.Sessions, 1)
	s := result.Sessions[0]
	assert.Equal(t, 1, s.Turns, "the session submitted on its first turn")
	assert.Equal(t, int64(2), s.Requests)
	assert.Equal(t, int64(500), s.InputTokens)
	assert.Equal(t, int64(400), s.CacheReadTokens)
	assert.Equal(t, int64(75), s.OutputTokens)
	assert.Equal(t, int64(30), s.ReasoningTokens)
	require.NotNil(t, s.ReportedCostMicros)
	assert.Equal(t, cost, *s.ReportedCostMicros)
	assert.Equal(t, llmusage.BillingMetered, s.BillingMode, "the route's billing mode")
	assert.Equal(t, []llmusage.ToolUsage{{Tool: review.SubmitReviewToolName, Calls: 1, DurationMs: s.Tools[0].DurationMs, ResultBytes: int64(len("review recorded"))}}, s.Tools)
	assert.Equal(t, 0, s.Elisions, "no context budget was set, so nothing was elided")
	assert.GreaterOrEqual(t, s.DurationMilli, int64(0))
}

func TestRun_LargeChangeIsSplitAndEachSessionOwnsItsPart(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 600, "b/two.go": 600, "c/three.go": 600})
	provider := &fakeProvider{respond: func(system, user string) (review.Review, error) {
		if system == seamsPrompt {
			return review.Review{Verdict: review.VerdictApprove, Summary: "seams fit"}, nil
		}
		owned := ownedFiles(user)
		return review.Review{Verdict: review.VerdictApproveWithComments, Summary: "part", Findings: []review.Finding{
			{File: owned[0], Line: 2, Severity: review.SeverityMinor, Summary: "issue in " + owned[0], Detail: "d"},
		}}, nil
	}}

	result, err := newOrchestrator(provider, Options{ChunkDiffBytes: 700, SeamPass: true}).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	prompts := provider.userPrompts("STANDARD")
	require.Len(t, prompts, 3)
	var owned []string
	for _, p := range prompts {
		mine := ownedFiles(p)
		require.Len(t, mine, 1)
		owned = append(owned, mine...)
		assert.Contains(t, p, "split into 3 parts")
		for i := range scope.Files {
			f := &scope.Files[i]
			assert.Contains(t, p, f.Path, "every session sees the whole manifest")
			if f.Path == mine[0] {
				assert.Contains(t, p, f.Block, "its own diff is inlined")
			} else {
				assert.NotContains(t, p, f.Block, "other parts' diffs are fetched on demand, not inlined")
			}
		}
	}
	assert.ElementsMatch(t, []string{"a/one.go", "b/two.go", "c/three.go"}, owned)

	require.Len(t, result.Review.Findings, 3)
	for _, f := range result.Review.Findings {
		assert.Equal(t, []string{"standard"}, f.Personas)
	}
	assert.Equal(t, review.VerdictApproveWithComments, result.Review.Verdict)
	assert.Len(t, result.Sessions, 4, "three parts and the seam pass")
	assert.Contains(t, result.Review.Summary, "part 2 of 3")
}

func TestRun_SeamPassSeesEveryPartsFindings(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 600, "b/two.go": 600})
	provider := &fakeProvider{respond: func(system, user string) (review.Review, error) {
		if system == seamsPrompt {
			return review.Review{Verdict: review.VerdictRequestChanges, Summary: "mismatch", Findings: []review.Finding{
				{File: "b/two.go", Line: 1, Severity: review.SeverityMajor, Summary: "caller assumes the old tag", Detail: "d"},
			}}, nil
		}
		owned := ownedFiles(user)
		return review.Review{Verdict: review.VerdictApproveWithComments, Summary: "part", Findings: []review.Finding{
			{File: owned[0], Severity: review.SeverityNit, Summary: "nit in " + owned[0], Detail: "d"},
		}}, nil
	}}

	result, err := newOrchestrator(provider, Options{ChunkDiffBytes: 700, SeamPass: true}).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	seamPrompts := provider.userPrompts(seamsPrompt)
	require.Len(t, seamPrompts, 1)
	assert.Contains(t, seamPrompts[0], "standard, part 1 of 2: a/one.go")
	assert.Contains(t, seamPrompts[0], "[nit] a/one.go: nit in a/one.go")
	assert.Contains(t, seamPrompts[0], "[nit] b/two.go: nit in b/two.go")
	assert.NotContains(t, seamPrompts[0], "+xxx", "the seam pass fetches diffs itself")

	require.NotEmpty(t, result.Review.Findings)
	assert.Equal(t, "caller assumes the old tag", result.Review.Findings[0].Summary, "most severe first")
	assert.Equal(t, []string{SeamsPersona}, result.Review.Findings[0].Personas)
	assert.Equal(t, review.VerdictRequestChanges, result.Review.Verdict)
}

func TestRun_NoSeamPassWhenDisabledOrNotSplit(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 600, "b/two.go": 600})

	disabled := &fakeProvider{respond: approve}
	_, err := newOrchestrator(disabled, Options{ChunkDiffBytes: 700, SeamPass: false}).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)
	assert.Empty(t, disabled.userPrompts(seamsPrompt))

	whole := &fakeProvider{respond: approve}
	_, err = newOrchestrator(whole, Options{ChunkDiffBytes: 700, SeamPass: true}).Run(context.Background(), scope, []*persona.Persona{security})
	require.NoError(t, err)
	assert.Empty(t, whole.userPrompts(seamsPrompt), "a whole-change persona is never split")
	assert.Len(t, whole.requests, 1)
}

func TestRun_WholePersonaDefersWhatDoesNotFitInline(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 600, "b/two.go": 600})
	provider := &fakeProvider{respond: approve}

	result, err := newOrchestrator(provider, Options{InlineDiffBytes: 700}).Run(context.Background(), scope, []*persona.Persona{security})
	require.NoError(t, err)

	prompt := provider.requests[0].Messages[1].Content
	assert.Contains(t, prompt, scope.Files[0].Block)
	assert.NotContains(t, prompt, scope.Files[1].Block, "whole files only: never cut off mid-file")
	assert.Contains(t, prompt, "not inlined above")
	assert.Contains(t, prompt, "- b/two.go\n")

	require.Len(t, result.Review.Warnings, 1, "the deferred file was never fetched")
	assert.Contains(t, result.Review.Warnings[0], "never looked at by this review: b/two.go")
	assert.True(t, result.Review.Incomplete, "the approval did not cover b/two.go")
	assert.True(t, result.Sessions[0].Incomplete)
	assert.Equal(t, review.VerdictIncomplete, result.Review.Verdict, "an approval that skipped a file is not an approval")
}

func TestRun_FailedSessionMakesTheReviewIncompleteButKeepsTheRest(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: func(system, _ string) (review.Review, error) {
		if system == "SECURITY" {
			return review.Review{}, errors.New("rate limited")
		}
		return review.Review{Verdict: review.VerdictApproveWithComments, Summary: "ok", Findings: []review.Finding{
			{File: "a/one.go", Severity: review.SeverityMinor, Summary: "kept", Detail: "d"},
		}}, nil
	}}

	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard, security})
	require.NoError(t, err)

	assert.True(t, result.Review.Incomplete)
	require.Len(t, result.Review.Findings, 1)
	assert.Equal(t, "kept", result.Review.Findings[0].Summary)
	require.NotEmpty(t, result.Review.Warnings)
	assert.Contains(t, result.Review.Warnings[0], "security was not reviewed")
	assert.Contains(t, result.Review.Warnings[0], "rate limited")
	assert.Contains(t, result.Sessions[1].Error, "rate limited")
	assert.Equal(t, review.VerdictIncomplete, result.Review.Verdict)
}

func TestRun_IncompleteDoesNotMaskARequestForChanges(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: func(system, _ string) (review.Review, error) {
		if system == "SECURITY" {
			return review.Review{}, errors.New("rate limited")
		}
		return review.Review{Verdict: review.VerdictRequestChanges, Summary: "bad", Findings: []review.Finding{
			{File: "a/one.go", Severity: review.SeverityBlocker, Summary: "blocked", Detail: "d"},
		}}, nil
	}}

	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard, security})
	require.NoError(t, err)
	assert.True(t, result.Review.Incomplete)
	assert.Equal(t, review.VerdictRequestChanges, result.Review.Verdict)
}

func TestRun_IncompleteOutranksApproveWithComments(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: func(system, _ string) (review.Review, error) {
		if system == "SECURITY" {
			return review.Review{}, errors.New("rate limited")
		}
		return review.Review{Verdict: review.VerdictApproveWithComments, Summary: "ok", Findings: []review.Finding{
			{File: "a/one.go", Severity: review.SeverityNit, Summary: "nit", Detail: "d"},
		}}, nil
	}}
	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard, security})
	require.NoError(t, err)
	assert.Equal(t, review.VerdictIncomplete, result.Review.Verdict)
}

func TestRun_CompleteApprovalStaysApprove(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	result, err := newOrchestrator(&fakeProvider{respond: approve}, Options{}).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)
	assert.Equal(t, review.VerdictApprove, result.Review.Verdict)
}

func TestRun_ModelCannotSubmitIncompleteVerdict(t *testing.T) {
	r := review.Review{Verdict: review.VerdictIncomplete, Summary: "s"}
	assert.Error(t, r.Validate())
	assert.NoError(t, r.ValidateOutput())
}

func TestRun_EverySessionFailingIsAnError(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: func(string, string) (review.Review, error) {
		return review.Review{}, errors.New("provider down")
	}}

	_, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard, security})
	assert.ErrorContains(t, err, "every review session failed")
	assert.ErrorContains(t, err, "provider down")
}

func TestRun_RespectsParallelism(t *testing.T) {
	scope := fixture(t, map[string]int{"a/1.go": 600, "b/2.go": 600, "c/3.go": 600, "d/4.go": 600, "e/5.go": 600})
	provider := &fakeProvider{respond: approve, overlap: 2, hold: 50 * time.Millisecond}

	_, err := newOrchestrator(provider, Options{ChunkDiffBytes: 700, Parallelism: 2}).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	assert.Len(t, provider.requests, 5)
	assert.Equal(t, 2, provider.maxSeen, "sessions run concurrently, but never more than Parallelism at once")
}

func TestRun_DuplicateFindingsAcrossPersonasAreMergedAndCredited(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: func(system, _ string) (review.Review, error) {
		severity := review.SeverityMinor
		if system == "SECURITY" {
			severity = review.SeverityBlocker
		}
		return review.Review{Verdict: review.VerdictApproveWithComments, Summary: "s", Findings: []review.Finding{
			{File: "a/one.go", Line: 4, Severity: severity, Summary: "write endpoint is missing its admin check", Detail: system + " detail"},
		}}, nil
	}}

	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard, security})
	require.NoError(t, err)

	require.Len(t, result.Review.Findings, 1)
	f := result.Review.Findings[0]
	assert.Equal(t, review.SeverityBlocker, f.Severity, "the more severe rating wins")
	assert.Equal(t, "SECURITY detail", f.Detail)
	assert.Equal(t, []string{"security", "standard"}, f.Personas)
	assert.Equal(t, review.VerdictRequestChanges, result.Review.Verdict)
}

// TestRun_MergedFindingsGetDistinctIDs checks that every finding surviving
// the merge is assigned a random, non-nil id -- what
// the report server's ingest contract (report.Finding.ID) and later
// `slop-review label <id>` both rely on -- and that merging two personas'
// reports of the same issue produces one finding with one id, not two.
func TestRun_MergedFindingsGetDistinctIDs(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: func(system, _ string) (review.Review, error) {
		return review.Review{Verdict: review.VerdictApproveWithComments, Summary: "s", Findings: []review.Finding{
			{File: "a/one.go", Line: 4, Severity: review.SeverityMinor, Summary: "same issue either way", Detail: system + " detail"},
		}}, nil
	}}

	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard, security})
	require.NoError(t, err)

	require.Len(t, result.Review.Findings, 1, "the duplicate report from both personas must merge into one finding, one id")
	assert.NotEqual(t, uuid.Nil, result.Review.Findings[0].ID)
}

func TestRun_FindingsAboutNonexistentFilesAreDropped(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: func(string, string) (review.Review, error) {
		return review.Review{Verdict: review.VerdictRequestChanges, Summary: "s", Findings: []review.Finding{
			{File: "a/one.go", Line: 999, Severity: review.SeverityMinor, Summary: "real file, impossible line", Detail: "d"},
			{File: "internal/imaginary.go", Line: 3, Severity: review.SeverityBlocker, Summary: "fabricated", Detail: "d"},
		}}, nil
	}}

	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	require.Len(t, result.Review.Findings, 1)
	assert.Equal(t, "real file, impossible line", result.Review.Findings[0].Summary)
	assert.Equal(t, 0, result.Review.Findings[0].Line)
	assert.Contains(t, result.Review.Findings[0].Detail, "cited line 999, past the end of this 10-line file")
	require.Len(t, result.Review.Warnings, 1)
	assert.Contains(t, result.Review.Warnings[0], "internal/imaginary.go")
	// The verdict was the reviewer's own call, and stands: dropping a
	// finding doesn't make the reviewer's judgement of the rest wrong.
	assert.Equal(t, review.VerdictRequestChanges, result.Review.Verdict)
}

func TestRun_FindingAboutADeletedFileIsKept(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	scope.Files = append(scope.Files, gitctx.FileDiff{Path: "gone/old.go", Deleted: 5, Block: "diff --git a/gone/old.go b/gone/old.go\n"})
	provider := &fakeProvider{respond: func(string, string) (review.Review, error) {
		return review.Review{Verdict: review.VerdictApproveWithComments, Summary: "s", Findings: []review.Finding{
			{File: "gone/old.go", Severity: review.SeverityMinor, Summary: "callers still need this", Detail: "d"},
		}}, nil
	}}

	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)
	require.Len(t, result.Review.Findings, 1)
}

func TestRun_SameWordingOnDistantLinesIsTwoFindings(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: func(system, _ string) (review.Review, error) {
		line := 2
		if system == "SECURITY" {
			line = 9
		}
		return review.Review{Verdict: review.VerdictApproveWithComments, Summary: "s", Findings: []review.Finding{
			{File: "a/one.go", Line: line, Severity: review.SeverityMinor, Summary: "missing error handling", Detail: "d"},
		}}, nil
	}}

	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard, security})
	require.NoError(t, err)
	assert.Len(t, result.Review.Findings, 2, "identical wording about two different places is two issues")
}

func farFindings(summaryFor func(system string) string) func(string, string) (review.Review, error) {
	return func(system, _ string) (review.Review, error) {
		line := 2
		if system == "SECURITY" {
			line = 9
		}
		return review.Review{Verdict: review.VerdictApproveWithComments, Summary: "s", Findings: []review.Finding{
			{File: "a/one.go", Line: line, Severity: review.SeverityMinor, Summary: summaryFor(system), Detail: "d"},
		}}, nil
	}
}

func TestRun_SpecificNearIdenticalFindingsOnDistantLinesMerge(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: farFindings(func(system string) string {
		if system == "SECURITY" {
			return "counter map is read and written by the worker goroutines without holding the mutex"
		}
		return "counter map is read and written by worker goroutines without holding the mutex lock"
	})}

	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard, security})
	require.NoError(t, err)
	require.Len(t, result.Review.Findings, 1)
	assert.Equal(t, []string{"security", "standard"}, result.Review.Findings[0].Personas)
	assert.NotZero(t, result.Review.Findings[0].Line, "the citation survived, so the lines really were far apart")
}

func TestRun_FindingsDifferingInTheirLastWordsStayTwo(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: farFindings(func(system string) string {
		if system == "SECURITY" {
			return "user supplied path is joined onto the upload directory and permits traversal outside"
		}
		return "user supplied path is joined onto the upload directory and leaks absolute locations"
	})}

	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard, security})
	require.NoError(t, err)
	assert.Len(t, result.Review.Findings, 2)
}

func TestRun_ScopeRelativeFindingPathsAreResolvedNotDropped(t *testing.T) {
	scope := fixture(t, map[string]int{"svc/server/api.go": 200, "svc/web/app.ts": 200})
	scope.ScopePath = "svc/"
	provider := &fakeProvider{respond: func(string, string) (review.Review, error) {
		return review.Review{Verdict: review.VerdictApproveWithComments, Summary: "s", Findings: []review.Finding{
			{File: "server/api.go", Line: 2, Severity: review.SeverityMinor, Summary: "relative to the review dir", Detail: "d"},
			{File: "./web/app.ts", Line: 3, Severity: review.SeverityMinor, Summary: "also relative", Detail: "d"},
			{File: "api.go", Line: 4, Severity: review.SeverityMinor, Summary: "bare basename of a changed file", Detail: "d"},
		}}, nil
	}}

	result, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	var files []string
	for _, f := range result.Review.Findings {
		files = append(files, f.File)
	}
	assert.ElementsMatch(t, []string{"svc/server/api.go", "svc/web/app.ts", "svc/server/api.go"}, files)
	assert.Empty(t, result.Review.Warnings)
}

var errQuota = errors.New("usage limit reached")

// quotaTrigger falls back on errQuota alone, as a config naming only
// usage_limit would.
func quotaTrigger(err error) string {
	if errors.Is(err, errQuota) {
		return "usage_limit"
	}
	return ""
}

func withFallback(fallback llm.LLMProvider, opts Options) Options {
	opts.Fallback = &Route{Name: "fallback", Provider: fallback, Model: "fm", BillingMode: llmusage.BillingSubscription}
	opts.Trigger = quotaTrigger
	return opts
}

func failing(err error) *fakeProvider {
	return &fakeProvider{respond: func(string, string) (review.Review, error) { return review.Review{}, err }}
}

func TestRun_FallbackTriggersOnTheConfiguredClassesOnly(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})

	fallback := &fakeProvider{respond: approve}
	_, err := newOrchestrator(failing(errors.New("rate limited")), withFallback(fallback, Options{})).
		Run(context.Background(), scope, []*persona.Persona{standard})
	require.Error(t, err, "a failure that isn't a trigger fails as it always did")
	assert.Empty(t, fallback.requests)

	fallback = &fakeProvider{respond: approve}
	result, err := newOrchestrator(failing(fmt.Errorf("session: %w", errQuota)), withFallback(fallback, Options{})).
		Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)
	assert.Len(t, fallback.requests, 1, "retried once, on the fallback")
	assert.Equal(t, &FellBack{From: "primary", To: "fallback", Class: "usage_limit"}, result.FellBack)
	require.Len(t, result.Sessions, 1)
	assert.Equal(t, "fallback", result.Sessions[0].Provider)
	assert.Equal(t, llmusage.BillingSubscription, result.Sessions[0].BillingMode, "a session retried on the fallback is billed as the fallback")
	assert.Equal(t, "fm", result.Sessions[0].Model)
	assert.Empty(t, result.Sessions[0].Error)
}

func TestRun_NoFallbackMeansNothingIsRecordedAsFallingBack(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	result, err := newOrchestrator(&fakeProvider{respond: approve}, withFallback(&fakeProvider{respond: approve}, Options{})).
		Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)
	assert.Nil(t, result.FellBack)
	assert.Equal(t, "primary", result.Sessions[0].Provider)
	assert.Equal(t, llmusage.BillingMetered, result.Sessions[0].BillingMode)
	assert.Equal(t, "m", result.Sessions[0].Model)
}

// A session that already succeeded on the primary keeps its review; the
// one that hit the quota is redone, whole, on the fallback.
func TestRun_FinishedSessionsAreKeptAndTheFailedOneMoves(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 600, "b/two.go": 600})
	aDone := make(chan struct{})
	primary := &fakeProvider{respond: func(_, user string) (review.Review, error) {
		if ownedFiles(user)[0] == "a/one.go" {
			defer close(aDone)
			return review.Review{Verdict: review.VerdictApproveWithComments, Summary: "from primary", Findings: []review.Finding{
				{File: "a/one.go", Severity: review.SeverityNit, Summary: "primary's finding", Detail: "d"},
			}}, nil
		}
		<-aDone
		return review.Review{}, errQuota
	}}
	fallback := &fakeProvider{respond: func(_, user string) (review.Review, error) {
		return review.Review{Verdict: review.VerdictApprove, Summary: "from fallback"}, nil
	}}

	result, err := newOrchestrator(primary, withFallback(fallback, Options{ChunkDiffBytes: 700, Parallelism: 2})).
		Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	byFile := map[string]SessionReport{}
	for _, s := range result.Sessions {
		byFile[s.Files[0]] = s
	}
	assert.Equal(t, "primary", byFile["a/one.go"].Provider)
	assert.Equal(t, "from primary", byFile["a/one.go"].Summary)
	assert.Equal(t, "fallback", byFile["b/two.go"].Provider)
	assert.Equal(t, "from fallback", byFile["b/two.go"].Summary)
	require.Len(t, result.Review.Findings, 1)
	assert.Equal(t, "primary's finding", result.Review.Findings[0].Summary)
	assert.Len(t, fallback.userPrompts("STANDARD"), 1, "only the failed part is redone")
	assert.NotNil(t, result.FellBack)
}

// Once the primary has hit its quota, sessions not yet started go straight
// to the fallback: a run doesn't keep hitting a primary it knows is out.
func TestRun_SessionsNotYetStartedGoStraightToTheFallback(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 600, "b/two.go": 600, "c/three.go": 600})
	primary := failing(errQuota)
	fallback := &fakeProvider{respond: approve}

	result, err := newOrchestrator(primary, withFallback(fallback, Options{ChunkDiffBytes: 700, Parallelism: 1, SeamPass: true})).
		Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	assert.Len(t, primary.requests, 1, "only the first session tried the primary")
	assert.Len(t, fallback.userPrompts("STANDARD"), 3)
	assert.Len(t, fallback.userPrompts(seamsPrompt), 1, "the seam pass runs on the fallback too")
	for _, s := range result.Sessions {
		assert.Equal(t, "fallback", s.Provider)
	}
}

func TestRun_TheFallbackFailingTooFailsTheRun(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	result, err := newOrchestrator(failing(errQuota), withFallback(failing(errors.New("out of credits")), Options{})).
		Run(context.Background(), scope, []*persona.Persona{standard})
	require.Error(t, err)
	assert.ErrorContains(t, err, "every review session failed")
	assert.ErrorContains(t, err, "out of credits")
	require.NotNil(t, result, "the failure still says where the sessions ran")
	assert.Equal(t, "usage_limit", result.FellBack.Class)
	assert.Equal(t, "fallback", result.Sessions[0].Provider)
}

// A fallback failing with the trigger class is not retried again: there
// is nowhere further to go.
func TestRun_TheFallbackIsNeverRetried(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	fallback := failing(errQuota)
	_, err := newOrchestrator(failing(errQuota), withFallback(fallback, Options{})).
		Run(context.Background(), scope, []*persona.Persona{standard})
	require.Error(t, err)
	assert.Len(t, fallback.requests, 1)
}

func TestRun_StartOnFallbackNeverTouchesThePrimary(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	primary := &fakeProvider{respond: approve}
	opts := withFallback(&fakeProvider{respond: approve}, Options{})
	opts.StartOnFallback = "auth"

	result, err := newOrchestrator(primary, opts).Run(context.Background(), scope, []*persona.Persona{standard, security})
	require.NoError(t, err)
	assert.Empty(t, primary.requests)
	assert.Equal(t, &FellBack{From: "primary", To: "fallback", Class: "auth"}, result.FellBack)
}

type fakeSession struct{ stats session.Stats }

func (f *fakeSession) Run(context.Context) (review.Review, error) {
	return review.Review{Verdict: review.VerdictApprove, Summary: "from the fake"}, nil
}
func (f *fakeSession) Stats() session.Stats { return f.stats }

// recordingFactory records the Spec it was handed, and fails the test if
// the route's Provider is used instead.
type recordingFactory struct {
	specs []*session.Spec
	stats session.Stats
}

func (f *recordingFactory) New(spec *session.Spec) session.Session {
	f.specs = append(f.specs, spec)
	return &fakeSession{stats: f.stats}
}

func TestRun_UsesTheRoutesSessionFactory(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200, "b/two.go": 200})
	provider := &fakeProvider{respond: approve}
	factory := &recordingFactory{stats: session.Stats{Effort: "high", Tokens: 42}}
	o := newOrchestrator(provider, Options{})
	o.opts.Primary.Sessions = factory

	result, err := o.Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	assert.Empty(t, provider.requests, "the route's provider is bypassed")
	assert.Equal(t, "from the fake", result.Review.Summary)
	require.Len(t, factory.specs, 1)
	spec := factory.specs[0]
	assert.Equal(t, scope.RepoRoot, spec.RepoRoot)
	assert.Equal(t, withGuidance("STANDARD"), spec.SystemPrompt)
	assert.Contains(t, spec.UserPrompt, "+xxx")
	require.NotNil(t, spec.Tools)
	assert.Empty(t, spec.Tools.Unseen(), "files inlined in the prompt are marked seen")
}
