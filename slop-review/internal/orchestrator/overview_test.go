package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
	"github.com/lowaa/slop-factory/slop-review/internal/persona"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

var blastRadius = &persona.Persona{Name: "blast-radius", Prompt: "BLAST", Chunking: persona.ChunkWhole, Stage: persona.StageOverview}

const overviewMap = "url -> ci.sh -> BROKEN"

// respondByPersona answers the overview persona with a map and one
// confirmed break, and everyone else with approve.
func respondByPersona(system, _ string) (review.Review, error) {
	if system == "BLAST" {
		return review.Review{
			Verdict: review.VerdictRequestChanges, Summary: overviewMap,
			Findings: []review.Finding{{File: "a/one.go", Line: 1, Severity: review.SeverityMajor, Summary: "stale url", Detail: "d"}},
		}, nil
	}
	return approve(system, "")
}

func overviewOpts(cacheDir string) Options {
	return Options{OverviewPersonas: []*persona.Persona{blastRadius}, OverviewCacheDir: cacheDir}
}

func TestRun_OverviewRunsFirstAndFeedsEveryOtherSession(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200, "b/two.go": 200})
	provider := &fakeProvider{respond: respondByPersona}

	result, err := newOrchestrator(provider, overviewOpts("")).Run(context.Background(), scope, []*persona.Persona{standard, security})
	require.NoError(t, err)

	require.Len(t, provider.requests, 3)
	assert.Equal(t, "BLAST", baseSystem(provider.requests[0].Messages[0].Content), "the overview stage runs before any other session")
	assert.NotContains(t, provider.requests[0].Messages[1].Content, "Blast radius", "the overview is not fed its own output")
	for _, system := range []string{"STANDARD", "SECURITY"} {
		prompt := provider.userPrompts(system)[0]
		assert.Contains(t, prompt, "Blast radius")
		assert.Contains(t, prompt, overviewMap)
		assert.Contains(t, prompt, "a/one.go:1: stale url", "its confirmed breaks too")
	}
	require.Len(t, result.Review.Findings, 1, "the creating run reports the overview's findings")
	assert.Equal(t, []string{"blast-radius"}, result.Review.Findings[0].Personas)
}

func TestRun_NoOverviewMeansNoBlastRadiusSection(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: respondByPersona}

	_, err := newOrchestrator(provider, Options{}).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	require.Len(t, provider.requests, 1)
	assert.NotContains(t, provider.requests[0].Messages[1].Content, "Blast radius")
}

func TestRun_OverviewIsCachedPerDiff(t *testing.T) {
	cache := t.TempDir()
	scope := fixture(t, map[string]int{"a/one.go": 200})
	run := func(scope2 *gitctx.Scope, p []*persona.Persona) (*fakeProvider, *Result) {
		provider := &fakeProvider{respond: respondByPersona}
		result, err := newOrchestrator(provider, overviewOpts(cache)).Run(context.Background(), scope2, p)
		require.NoError(t, err)
		return provider, result
	}
	overviewCalls := func(p *fakeProvider) int { return len(p.userPrompts("BLAST")) }

	first, _ := run(scope, []*persona.Persona{standard})
	assert.Equal(t, 1, overviewCalls(first), "miss: computed")

	second, result := run(scope, []*persona.Persona{security})
	assert.Equal(t, 0, overviewCalls(second), "hit: any persona over the same diff reuses it")
	assert.Contains(t, second.userPrompts("SECURITY")[0], overviewMap, "and still feeds it to the session")
	assert.Empty(t, result.Review.Findings, "a cached overview's findings were reported by the run that created it")
	assert.Len(t, result.Sessions, 1)

	edited := *scope
	edited.Files = append([]gitctx.FileDiff(nil), scope.Files...)
	edited.Files[0].Block += "+one more line\n"
	third, _ := run(&edited, []*persona.Persona{standard})
	assert.Equal(t, 1, overviewCalls(third), "a different diff, same repository, is a miss")

	elsewhere := *scope
	elsewhere.RepoRoot = t.TempDir()
	fourth, _ := run(&elsewhere, []*persona.Persona{standard})
	assert.Equal(t, 1, overviewCalls(fourth), "the same diff in another repository is a miss")

	rerun := &fakeProvider{respond: func(system, user string) (review.Review, error) {
		return respondByPersona("BLAST", user)
	}}
	opts := overviewOpts(cache)
	edit := *blastRadius
	edit.Prompt = "BLAST2"
	opts.OverviewPersonas = []*persona.Persona{&edit}
	_, err := newOrchestrator(rerun, opts).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)
	assert.Equal(t, 1, len(rerun.userPrompts("BLAST2")), "editing the persona invalidates it")
}

func TestRun_ExplicitOverviewPersonaAlwaysRuns(t *testing.T) {
	cache := t.TempDir()
	scope := fixture(t, map[string]int{"a/one.go": 200})
	for range 2 {
		provider := &fakeProvider{respond: respondByPersona}
		result, err := newOrchestrator(provider, overviewOpts(cache)).Run(context.Background(), scope, []*persona.Persona{blastRadius})
		require.NoError(t, err)
		assert.Len(t, provider.userPrompts("BLAST"), 1)
		assert.Len(t, result.Review.Findings, 1)
	}
}

func TestRun_OverviewSeesTheWholeChangeWhateverTheReviewScope(t *testing.T) {
	full := fixture(t, map[string]int{"a/one.go": 200, "b/two.go": 200})
	scoped := fixture(t, map[string]int{"a/one.go": 200})
	scoped.RepoRoot = full.RepoRoot
	opts := overviewOpts("")
	opts.FullScope = full
	provider := &fakeProvider{respond: respondByPersona}

	_, err := newOrchestrator(provider, opts).Run(context.Background(), scoped, []*persona.Persona{standard})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"a/one.go", "b/two.go"}, ownedFiles(provider.userPrompts("BLAST")[0]))
	assert.Equal(t, []string{"a/one.go"}, ownedFiles(provider.userPrompts("STANDARD")[0]))
}

func TestRun_FailedOverviewCostsTheSectionNotTheReview(t *testing.T) {
	scope := fixture(t, map[string]int{"a/one.go": 200})
	provider := &fakeProvider{respond: func(system, user string) (review.Review, error) {
		if system == "BLAST" {
			return review.Review{}, errors.New("boom")
		}
		return approve(system, user)
	}}

	result, err := newOrchestrator(provider, overviewOpts(t.TempDir())).Run(context.Background(), scope, []*persona.Persona{standard})
	require.NoError(t, err)

	assert.NotContains(t, provider.userPrompts("STANDARD")[0], "Blast radius")
	assert.True(t, result.Review.Incomplete)
	assert.True(t, strings.Contains(strings.Join(result.Review.Warnings, "\n"), "blast-radius was not reviewed"))
}
