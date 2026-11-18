package runreport

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/config"
	"github.com/lowaa/slop-factory/slop-review/internal/fingerprint"
	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/orchestrator"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
	"github.com/lowaa/slop-factory/slop-review/report"
)

// fixedInput is a hand-built set of inputs covering every field Build
// maps, so the test can assert on the exact report.Run produced rather
// than just "it didn't crash".
func fixedInput() Input {
	findingID := uuid.MustParse("0b9f8e7d-6c5b-4a39-8281-7f6e5d4c3b2a")
	started := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	finished := started.Add(65 * time.Second)

	return Input{
		Result: &orchestrator.Result{
			Review: review.Review{
				Verdict:    review.VerdictRequestChanges,
				Summary:    "Merged from 2 review sessions.",
				Incomplete: true,
				Warnings:   []string{"standard: 1 changed file(s) were never looked at by this review: b.go"},
				Findings: []review.Finding{
					{
						ID: findingID, File: "a.go", Line: 3, Severity: review.SeverityMajor,
						Summary: "nil dereference", Detail: "d", Personas: []string{"standard"},
					},
				},
			},
			Sessions: []orchestrator.SessionReport{
				{
					Persona: "security", Part: 1, Parts: 1, Files: []string{"a.go"},
					Verdict: review.VerdictApprove, Summary: "fine", Findings: 0,
					Turns: 3, Elisions: 0, InputTokens: 1200, OutputTokens: 300, DurationMilli: 4100,
				},
				{
					Persona: "standard", Part: 1, Parts: 1, Files: []string{"a.go", "b.go"},
					Verdict: review.VerdictRequestChanges, Summary: "a bug", Findings: 1,
					Incomplete: true, Turns: 40, Elisions: 2, UnseenFiles: []string{"b.go"},
					InputTokens: 90000, OutputTokens: 2100, DurationMilli: 61000,
				},
			},
		},
		Scope: &gitctx.Scope{
			Branch:     "feature",
			BaseRef:    "0123456789abcdef0123456789abcdef01234567",
			ScopePath:  "slop-review/",
			RemoteURL:  "git@github.com:acme/widget.git",
			HeadAuthor: "Ada Lovelace <ada@example.com>",
			Files: []gitctx.FileDiff{
				{Path: "a.go", Block: "diff --git a/a.go b/a.go\n+new line\n"},
			},
		},
		Cfg: &config.Config{
			Route:    config.Route{Provider: "openrouter", Model: "openai/gpt-5.6-luna-pro"},
			MaxTurns: 40, MaxDiffBytes: 200000, ChunkDiffBytes: 60000, ContextBudgetBytes: 400000,
			Parallelism: 4, SeamPass: true,
			ToolVersion: "c4ca88d", ToolVersionDirty: false,
		},
		Personas:   []string{"security", "standard"},
		HeadSHA:    "89abcdef0123456789abcdef0123456789abcdef",
		Dirty:      true,
		StartedAt:  started,
		FinishedAt: finished,
	}
}

func TestBuild_RecordsReasoningEffort(t *testing.T) {
	in := fixedInput()
	in.Cfg.Provider, in.Cfg.Model, in.Cfg.ReasoningEffort = "chatgpt", "gpt-5.6-luna", "high"

	r := Build(in)
	require.NoError(t, r.Validate())
	assert.Equal(t, "chatgpt", r.Provider)
	assert.Equal(t, "high", r.Config.ReasoningEffort)
}

func TestBuild_MapsEveryField(t *testing.T) {
	in := fixedInput()
	r := Build(in)

	require.NoError(t, r.Validate())

	assert.Equal(t, report.CompletedSchemaVersion, r.SchemaVersion)
	assert.NotEqual(t, uuid.Nil, r.ID)
	assert.Equal(t, report.KindReview, r.Kind)
	assert.Nil(t, r.Bench)

	assert.Equal(t, "git@github.com:acme/widget.git", r.RepoRemote)
	assert.Equal(t, "feature", r.Branch)
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", r.BaseRef)
	assert.Equal(t, "89abcdef0123456789abcdef0123456789abcdef", r.HeadSHA)
	assert.Equal(t, "Ada Lovelace <ada@example.com>", r.HeadAuthor)
	assert.True(t, r.Dirty)
	assert.Equal(t, "slop-review/", r.ScopePath)
	assert.Equal(t, in.Scope.Diff(), r.Diff)

	assert.Equal(t, report.ToolVersion{Kind: "git_sha", Value: "c4ca88d", Attributes: map[string]string{"dirty": "false"}}, r.ToolVersion)
	assert.Equal(t, "openrouter", r.Provider)
	assert.Equal(t, "openai/gpt-5.6-luna-pro", r.Model)
	assert.Equal(t, []string{"security", "standard"}, r.Personas)
	assert.Equal(t, report.Config{
		MaxTurns: 40, MaxDiffBytes: 200000, ChunkDiffBytes: 60000, ContextBudgetBytes: 400000,
		Parallelism: 4, SeamPass: true, AllowSecrets: false,
	}, r.Config)

	assert.Equal(t, "request_changes", r.Verdict)
	assert.Equal(t, "Merged from 2 review sessions.", r.Summary)
	assert.True(t, r.Incomplete)
	assert.Equal(t, []string{"standard: 1 changed file(s) were never looked at by this review: b.go"}, r.Warnings)

	require.Len(t, r.Sessions, 2)
	assert.Equal(t, report.Session{
		Persona: "security", Part: 1, Parts: 1, Files: []string{"a.go"},
		Verdict: "approve", Summary: "fine", Findings: 0,
		Turns: 3, Elisions: 0, InputTokens: 1200, OutputTokens: 300, DurationMilli: 4100,
	}, r.Sessions[0])
	assert.Equal(t, report.Session{
		Persona: "standard", Part: 1, Parts: 1, Files: []string{"a.go", "b.go"},
		Verdict: "request_changes", Summary: "a bug", Findings: 1,
		Incomplete: true, Turns: 40, Elisions: 2, UnseenFiles: []string{"b.go"},
		InputTokens: 90000, OutputTokens: 2100, DurationMilli: 61000,
	}, r.Sessions[1])

	require.Len(t, r.Findings, 1)
	assert.Equal(t, report.Finding{
		ID: uuid.MustParse("0b9f8e7d-6c5b-4a39-8281-7f6e5d4c3b2a"), File: "a.go", Line: 3, Severity: "major",
		Summary: "nil dereference", Detail: "d", Personas: []string{"standard"},
	}, r.Findings[0])

	assert.Equal(t, in.StartedAt, r.StartedAt)
	assert.Equal(t, in.FinishedAt, r.FinishedAt)
}

func TestBuild_CleanRunCarriesNoDiff(t *testing.T) {
	in := fixedInput()
	in.Dirty = false
	r := Build(in)
	require.NoError(t, r.Validate())
	assert.Empty(t, r.Diff, "a clean run's change is recoverable from base_ref/head_sha, so the diff must not be sent")
}

func TestBuild_EveryCallGetsAFreshID(t *testing.T) {
	in := fixedInput()
	a, b := Build(in), Build(in)
	assert.NotEqual(t, a.ID, b.ID)
}

func TestBuild_AllThreeBenchFlagsMakeItABenchRun(t *testing.T) {
	in := fixedInput()
	in.Cfg.BenchCase = "case1"
	in.Cfg.BenchVariant = "chunked"
	in.Cfg.BenchRun = 3

	r := Build(in)
	require.NoError(t, r.Validate())
	assert.Equal(t, report.KindBench, r.Kind)
	require.NotNil(t, r.Bench)
	assert.Equal(t, report.Bench{Case: "case1", Variant: "chunked", Run: 3}, *r.Bench)
}

func TestBuild_PartialBenchFlagsStayAReviewRun(t *testing.T) {
	in := fixedInput()
	in.Cfg.BenchCase = "case1" // variant/run left unset
	r := Build(in)
	require.NoError(t, r.Validate())
	assert.Equal(t, report.KindReview, r.Kind)
	assert.Nil(t, r.Bench)
}

func TestBuild_RepoRemoteFallsBackToScopeWhenNoOverride(t *testing.T) {
	in := fixedInput()
	in.Cfg.ReportRepo = ""
	r := Build(in)
	assert.Equal(t, "git@github.com:acme/widget.git", r.RepoRemote)
}

func TestBuild_RepoRemoteOverrideWins(t *testing.T) {
	in := fixedInput()
	in.Cfg.ReportRepo = "git@github.com:acme/fork.git"
	r := Build(in)
	assert.Equal(t, "git@github.com:acme/fork.git", r.RepoRemote, "--report-repo must override the scope's own remote")
}

// A credential embedded in the remote -- an HTTPS token, say -- must never
// reach the built report: it gets spooled to disk and posted to the server
// as-is, whichever source it came from.
func TestBuild_RepoRemoteIsRedacted(t *testing.T) {
	in := fixedInput()
	in.Scope.RemoteURL = "https://x-access-token:ghp_secrettoken@github.com/acme/widget.git"
	r := Build(in)
	assert.Equal(t, "https://github.com/acme/widget.git", r.RepoRemote)
	assert.NotContains(t, r.RepoRemote, "ghp_secrettoken")
}

func TestBuild_RepoRemoteOverrideIsRedactedToo(t *testing.T) {
	in := fixedInput()
	in.Cfg.ReportRepo = "https://ghp_secrettoken@github.com/acme/fork.git"
	r := Build(in)
	assert.Equal(t, "https://github.com/acme/fork.git", r.RepoRemote)
	assert.NotContains(t, r.RepoRemote, "ghp_secrettoken")
}

func TestBuild_ToolVersionDirtyAttributeReflectsFlag(t *testing.T) {
	in := fixedInput()
	in.Cfg.ToolVersionDirty = true
	r := Build(in)
	assert.Equal(t, "true", r.ToolVersion.Attributes["dirty"])
}

// Without --tool-version the build's own VCS stamp is used when there is
// one, and "unknown" otherwise -- never an empty version, which Validate
// refuses and which would lose the run.
func TestBuild_FallsBackToBuildInfoOrUnknownWhenNoToolVersionFlag(t *testing.T) {
	in := fixedInput()
	in.Cfg.ToolVersion = ""
	r := Build(in)
	assert.NotEmpty(t, r.ToolVersion.Kind)
	assert.NotEmpty(t, r.ToolVersion.Value)
	assert.Contains(t, []string{"git_sha", "unknown"}, r.ToolVersion.Kind)
	require.NoError(t, r.Validate(), "a run with no tool version flag must still be reportable")
}

func failedInput(err error, class report.FailureClass) *FailedInput {
	in := fixedInput()
	return &FailedInput{
		Err: err, Class: class, Cfg: in.Cfg, Scope: in.Scope, Personas: in.Personas,
		Host: "agent-vm-3", StartedAt: in.StartedAt, FinishedAt: in.FinishedAt,
	}
}

func TestBuildFailed_MapsWhatIsKnown(t *testing.T) {
	in := failedInput(fmt.Errorf("session: %w: limit", llm.ErrUsageLimit), report.FailureUsageLimit)
	in.Scope.HeadSHA = "89abcdef0123456789abcdef0123456789abcdef"
	r := BuildFailed(in)

	require.NoError(t, r.Validate())
	assert.Equal(t, report.FailedSchemaVersion, r.SchemaVersion, "a run on local settings is sent as before v4")
	assert.Equal(t, report.StatusFailed, r.Status)
	assert.Equal(t, &report.Failure{Class: report.FailureUsageLimit, Message: in.Err.Error(), Host: "agent-vm-3"}, r.Failure)
	assert.Equal(t, "openrouter", r.Provider)
	assert.Equal(t, "openai/gpt-5.6-luna-pro", r.Model)
	assert.Equal(t, "git@github.com:acme/widget.git", r.RepoRemote)
	assert.Equal(t, "feature", r.Branch)
	assert.Equal(t, in.Scope.HeadSHA, r.HeadSHA)
	assert.Equal(t, []string{"security", "standard"}, r.Personas)
	assert.Empty(t, r.Verdict)
	assert.Empty(t, r.Findings)
	assert.Empty(t, r.Diff, "a failed run reviewed nothing, so carries no diff")
}

// A run can fail before its scope is resolved: no repo, no branch, still a
// report.
func TestBuildFailed_BeforeTheScopeIsKnown(t *testing.T) {
	in := failedInput(errors.New("config: OPENROUTER_API_KEY is required"), report.FailureConfig)
	in.Scope, in.Personas = nil, nil
	r := BuildFailed(in)

	require.NoError(t, r.Validate())
	assert.Empty(t, r.RepoRemote)
	assert.Empty(t, r.Branch)
}

func TestBuildFailed_DropsARemoteTheServerCouldNotKey(t *testing.T) {
	in := failedInput(errors.New("boom"), report.FailureOther)
	in.Scope.RemoteURL = "/home/me/repo"
	r := BuildFailed(in)

	require.NoError(t, r.Validate())
	assert.Empty(t, r.RepoRemote)
}

func TestBuildFailed_ScrubsCredentialsFromTheMessage(t *testing.T) {
	in := failedInput(errors.New("401 for key sk-or-secret and token ingest-secret"), report.FailureAuth)
	in.Cfg.APIKey, in.Cfg.ReportToken = "sk-or-secret", "ingest-secret"
	r := BuildFailed(in)

	assert.NotContains(t, r.Failure.Message, "sk-or-secret")
	assert.NotContains(t, r.Failure.Message, "ingest-secret")
	assert.Contains(t, r.Failure.Message, "[redacted]")
}

func TestBuildFailed_ScrubsCredentialsFromURLsInTheMessage(t *testing.T) {
	in := failedInput(errors.New(`POST "https://user:hunter2@gateway.example/v1/chat/completions": 500; remote git@github.com:acme/widget.git`), report.FailureBackend)
	r := BuildFailed(in)

	assert.NotContains(t, r.Failure.Message, "hunter2")
	assert.Contains(t, r.Failure.Message, "https://[redacted]@gateway.example/v1")
	assert.Contains(t, r.Failure.Message, "git@github.com:acme/widget.git", "an scp-style remote carries no credential")
}

func TestBuildFailed_CutsALongMessageToTheContractsBound(t *testing.T) {
	r := BuildFailed(failedInput(errors.New(strings.Repeat("é", report.MaxTextBytes)), report.FailureOther))

	require.NoError(t, r.Validate())
	assert.True(t, utf8.ValidString(r.Failure.Message))
	assert.True(t, strings.HasSuffix(r.Failure.Message, "[truncated]"))
}

func TestClassify(t *testing.T) {
	joined := errors.Join(
		fmt.Errorf("security: %w", fmt.Errorf("%w: 502", llm.ErrBackend)),
		fmt.Errorf("standard: %w", fmt.Errorf("%w: limit", llm.ErrUsageLimit)),
	)
	for name, tc := range map[string]struct {
		err      error
		fallback report.FailureClass
		want     report.FailureClass
	}{
		"chatgpt usage limit":                          {fmt.Errorf("x: %w", llm.ErrUsageLimit), report.FailureOther, report.FailureUsageLimit},
		"chatgpt login":                                {fmt.Errorf("x: %w", llm.ErrAuth), report.FailureConfig, report.FailureAuth},
		"chatgpt backend":                              {fmt.Errorf("x: %w", llm.ErrBackend), report.FailureOther, report.FailureBackend},
		"openrouter key rejected":                      {fmt.Errorf("x: %w", llm.ErrKeyRejected), report.FailureOther, report.FailureAuth},
		"openrouter credits":                           {fmt.Errorf("x: %w", llm.ErrCredits), report.FailureOther, report.FailureCredits},
		"openrouter request":                           {fmt.Errorf("x: %w", llm.ErrRequest), report.FailureOther, report.FailureBackend},
		"no provider error":                            {errors.New("session: exceeded max turns"), report.FailureOther, report.FailureOther},
		"usage limit outranks backend across sessions": {joined, report.FailureOther, report.FailureUsageLimit},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, Classify(tc.err, tc.fallback))
		})
	}
}

// Under a central config, a run says which revision it ran under and where
// each session ran, which only v4 carries.
func TestBuild_RecordsTheCentralConfigAndWhereEachSessionRan(t *testing.T) {
	in := fixedInput()
	in.Cfg.ProviderConfigRevision = 5
	in.Cfg.Provider, in.Cfg.Model = "chatgpt", "gpt-5.6-luna"
	in.Result.Sessions[0].Provider, in.Result.Sessions[0].Model = "chatgpt", "gpt-5.6-luna"
	in.Result.Sessions[1].Provider, in.Result.Sessions[1].Model = "openrouter", "openai/gpt-5.6-luna"
	in.Result.FellBack = &orchestrator.FellBack{From: "chatgpt", To: "openrouter", Class: "usage_limit"}
	in.Host = "agent-vm-3"

	r := Build(in)
	require.NoError(t, r.Validate())
	assert.Equal(t, 4, r.SchemaVersion)
	assert.Equal(t, int64(5), r.ProviderConfigRevision)
	assert.Equal(t, &report.Fallback{From: "chatgpt", To: "openrouter", Class: report.FailureUsageLimit, Host: "agent-vm-3"}, r.Fallback)
	assert.Equal(t, "chatgpt", r.Sessions[0].Provider)
	assert.Equal(t, "openrouter", r.Sessions[1].Provider)
	assert.Equal(t, "openai/gpt-5.6-luna", r.Sessions[1].Model)
}

// Without a central config, nothing v4 added goes out, so a server that
// predates v4 still takes the run.
func TestBuild_LocalSettingsSendNothingV4Added(t *testing.T) {
	in := fixedInput()
	in.Result.Sessions[0].Provider, in.Result.Sessions[0].Model = "openrouter", "m"
	in.Host = "agent-vm-3"

	r := Build(in)
	assert.Equal(t, report.CompletedSchemaVersion, r.SchemaVersion)
	assert.Zero(t, r.ProviderConfigRevision)
	assert.Nil(t, r.Fallback)
	for _, s := range r.Sessions {
		assert.Empty(t, s.Provider)
		assert.Empty(t, s.Model)
	}
}

func TestBuildFailed_RecordsAFallbackThatFailedToo(t *testing.T) {
	in := failedInput(errors.New("every review session failed"), report.FailureCredits)
	in.Cfg.ProviderConfigRevision = 2
	in.Cfg.Provider = "chatgpt"
	in.FellBack = &orchestrator.FellBack{From: "chatgpt", To: "openrouter", Class: "usage_limit"}

	r := BuildFailed(in)
	require.NoError(t, r.Validate())
	assert.Equal(t, 4, r.SchemaVersion)
	assert.Equal(t, int64(2), r.ProviderConfigRevision)
	assert.Equal(t, &report.Fallback{From: "chatgpt", To: "openrouter", Class: report.FailureUsageLimit, Host: "agent-vm-3"}, r.Fallback)
}

// A session with usage makes the run v5, and its usage goes out in
// go-llm-usage's terms; one without (it never started) sends none.
func TestBuild_ASessionsUsageMakesTheRunV5(t *testing.T) {
	in := fixedInput()
	cost := int64(1831)
	in.Result.Sessions[0].BillingMode = llmusage.BillingMetered
	in.Result.Sessions[0].Requests = 4
	in.Result.Sessions[0].CacheReadTokens = 900
	in.Result.Sessions[0].ReasoningTokens = 120
	in.Result.Sessions[0].ReportedCostMicros = &cost
	in.Result.Sessions[0].Tools = []llmusage.ToolUsage{{Tool: "read_file", Calls: 2, Errors: 1, DurationMs: 12, ResultBytes: 5400}}

	r := Build(in)
	require.NoError(t, r.Validate())
	assert.Equal(t, report.UsageSchemaVersion, r.SchemaVersion)

	s := r.Sessions[0]
	assert.Equal(t, llmusage.BillingMetered, s.BillingMode)
	assert.Equal(t, int64(4), s.Requests)
	assert.Equal(t, int64(1200), s.InputTokens)
	assert.Equal(t, int64(900), s.CacheReadTokens)
	assert.Equal(t, int64(120), s.ReasoningTokens)
	assert.Equal(t, &cost, s.ReportedCostMicros)
	assert.Equal(t, in.Result.Sessions[0].Tools, s.Tools)

	assert.False(t, r.Sessions[1].HasUsage())
}

func testFingerprint() *fingerprint.Result {
	sec, std := "security text\n", "standard text\n"
	return &fingerprint.Result{
		Run: report.HashText("run"), Source: report.HashText("source"),
		Personas: map[string]fingerprint.Persona{
			"security": {Hash: report.HashText(sec), Text: sec},
			"standard": {Hash: report.HashText(std), Text: std},
			"unused":   {Hash: report.HashText("unused"), Text: "unused"},
		},
	}
}

func TestBuild_RecordsTheFingerprintAndEachSessionsPersona(t *testing.T) {
	in := fixedInput()
	in.Fingerprint = testFingerprint()

	r := Build(in)
	require.NoError(t, r.Validate())
	assert.Equal(t, report.FingerprintSchemaVersion, r.SchemaVersion)
	assert.Equal(t, in.Fingerprint.Run, r.Fingerprint)
	assert.Equal(t, in.Fingerprint.Source, r.SourceHash)
	assert.Equal(t, in.Fingerprint.Personas["security"].Hash, r.Sessions[0].PersonaHash)
	assert.Equal(t, in.Fingerprint.Personas["standard"].Hash, r.Sessions[1].PersonaHash)
	assert.Len(t, r.PersonaTexts, 2, "only the personas that ran are sent")
	assert.Equal(t, "security text\n", r.PersonaTexts[r.Sessions[0].PersonaHash])
}

func TestBuild_NoFingerprintSendsNothingV6Added(t *testing.T) {
	r := Build(fixedInput())
	assert.Equal(t, report.CompletedSchemaVersion, r.SchemaVersion)
	assert.Empty(t, r.Fingerprint)
	assert.Empty(t, r.PersonaTexts)
}

func TestBuild_ASessionWhosePersonaIsNotInTheLibraryHasNoHash(t *testing.T) {
	in := fixedInput()
	in.Fingerprint = testFingerprint()
	delete(in.Fingerprint.Personas, "security")
	r := Build(in)
	require.NoError(t, r.Validate())
	assert.Empty(t, r.Sessions[0].PersonaHash)
}

func TestBuildFailed_RecordsTheFingerprint(t *testing.T) {
	in := failedInput(errors.New("boom"), report.FailureBackend)
	in.Fingerprint = testFingerprint()
	r := BuildFailed(in)
	require.NoError(t, r.Validate())
	assert.Equal(t, in.Fingerprint.Run, r.Fingerprint)
}

// A codex run may quote a secret it read itself, so it is reported the way
// an --allow-secrets run is, which the server refuses to store; a claude
// run, denied the checkout's secrets, is not.
func TestReportConfig_FlagsARunWhoseFindingsCouldQuoteASecret(t *testing.T) {
	for provider, want := range map[string]bool{"codex": true, "claude": false, "chatgpt": false} {
		cfg, err := config.Load([]string{"--provider=" + provider})
		require.NoError(t, err)
		assert.Equal(t, want, reportConfig(cfg).AllowSecrets, provider)
	}
}
