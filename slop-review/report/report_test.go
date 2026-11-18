package report

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The golden fixture is shared with the report server's ingest tests, so a
// change to the contract that breaks the server shows up on both sides.
func loadGolden(t *testing.T) *Run {
	t.Helper()
	raw, err := os.ReadFile("testdata/run.json")
	require.NoError(t, err)
	var r Run
	require.NoError(t, json.Unmarshal(raw, &r))
	return &r
}

func TestGoldenRunIsValid(t *testing.T) {
	r := loadGolden(t)
	require.NoError(t, r.Validate())
	assert.Equal(t, KindReview, r.Kind)
	require.Len(t, r.Sessions, 2)
	assert.Equal(t, []string{"b.go"}, r.Sessions[1].UnseenFiles)
	assert.Equal(t, int64(90000), r.Sessions[1].InputTokens)
}

// Compared as raw JSON, field by field, so a field dropped or renamed in the
// Go types -- which a struct-to-struct comparison can't see, since both sides
// lose it alike -- fails here.
func TestGoldenRunRoundTripsEveryJSONField(t *testing.T) {
	raw, err := os.ReadFile("testdata/run.json")
	require.NoError(t, err)
	var r Run
	require.NoError(t, json.Unmarshal(raw, &r))
	out, err := json.Marshal(&r)
	require.NoError(t, err)

	var want, got map[string]any
	require.NoError(t, json.Unmarshal(raw, &want))
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, want, got)
}

func TestGoldenRunRoundTrips(t *testing.T) {
	r := loadGolden(t)
	out, err := json.Marshal(r)
	require.NoError(t, err)
	var again Run
	require.NoError(t, json.Unmarshal(out, &again))
	assert.Equal(t, r, &again)
}

func TestValidate_RejectsEachMalformation(t *testing.T) {
	cases := map[string]struct {
		mutate func(r *Run)
		want   string
	}{
		"unknown schema version":   {func(r *Run) { r.SchemaVersion = 1 }, "schema_version"},
		"no id":                    {func(r *Run) { r.ID = uuid.Nil }, "id is required"},
		"unknown kind":             {func(r *Run) { r.Kind = "adhoc" }, "kind"},
		"bench kind without bench": {func(r *Run) { r.Kind = KindBench }, "bench must be set"},
		"bench without bench kind": {func(r *Run) { r.Bench = &Bench{Case: "c", Variant: "v", Run: 1} }, "bench must be set"},
		"incomplete bench":         {func(r *Run) { r.Kind = KindBench; r.Bench = &Bench{Case: "c", Run: 1} }, "bench needs a case, a variant"},
		"no head sha":              {func(r *Run) { r.HeadSHA = " " }, "head_sha is required"},
		"unknown verdict":          {func(r *Run) { r.Verdict = "lgtm" }, "verdict"},
		"no personas":              {func(r *Run) { r.Personas = nil }, "personas is required"},
		"dirty without diff":       {func(r *Run) { r.Diff = "" }, "diff is required when dirty"},
		"clean with diff":          {func(r *Run) { r.Dirty = false }, "diff must be empty unless dirty"},
		"oversized diff":           {func(r *Run) { r.Diff = strings.Repeat("x", MaxDiffBytes+1) }, "diff is longer"},
		"no sessions":              {func(r *Run) { r.Sessions = nil }, "sessions must number"},
		"bad session part":         {func(r *Run) { r.Sessions[0].Part = 2 }, "is not a part"},
		"repeated finding id":      {func(r *Run) { r.Findings = append(r.Findings, r.Findings[0]) }, "is repeated"},
		"finding without id":       {func(r *Run) { r.Findings[0].ID = uuid.Nil }, "id is required"},
		"unknown severity":         {func(r *Run) { r.Findings[0].Severity = "catastrophic" }, "severity"},
		"negative line":            {func(r *Run) { r.Findings[0].Line = -1 }, "line must not be negative"},
		"finish before start":      {func(r *Run) { r.FinishedAt = r.StartedAt.Add(-1) }, "finish after start"},
		"missing repo_remote":      {func(r *Run) { r.RepoRemote = "" }, "repo_remote"},
		"local-path repo_remote":   {func(r *Run) { r.RepoRemote = "/home/me/repo" }, "repo_remote"},
		"credentialed repo_remote": {func(r *Run) { r.RepoRemote = "https://ghp_xxxSECRETxxx@github.com/example/widgets.git" }, "credentials in its userinfo"},
		"oversized head_author":    {func(r *Run) { r.HeadAuthor = strings.Repeat("x", maxShortString+1) }, "head_author is longer"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := loadGolden(t)
			tc.mutate(r)
			err := r.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestValidate_AcceptsAnIncompleteVerdict(t *testing.T) {
	r := loadGolden(t)
	r.Verdict = "incomplete"
	require.NoError(t, r.Validate())
}

func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	r := loadGolden(t)
	r.Verdict = "lgtm"
	r.Findings[0].Severity = "catastrophic"
	err := r.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verdict")
	assert.Contains(t, err.Error(), "severity")
}

// Every field within its own bound can still add up to more than the server
// accepts; Validate must catch that, since a report the server refuses is
// never retried.
func TestValidate_RejectsAReportOverTheTotalSizeLimit(t *testing.T) {
	r := loadGolden(t)
	detail := strings.Repeat("x", MaxTextBytes-1)
	r.Findings = nil
	for len(r.Findings) < MaxFindings {
		r.Findings = append(r.Findings, Finding{ID: uuid.New(), File: "a.go", Severity: "minor", Summary: "s", Detail: detail, Personas: []string{"standard"}})
	}
	err := r.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "over the")
}

func loadFailedGolden(t *testing.T) *Run {
	t.Helper()
	raw, err := os.ReadFile("testdata/failed_run.json")
	require.NoError(t, err)
	var r Run
	require.NoError(t, json.Unmarshal(raw, &r))
	return &r
}

func TestFailedGoldenRunIsValidAndRoundTrips(t *testing.T) {
	raw, err := os.ReadFile("testdata/failed_run.json")
	require.NoError(t, err)
	r := loadFailedGolden(t)
	require.NoError(t, r.Validate())
	assert.Equal(t, StatusFailed, r.Status)
	assert.Equal(t, FailureUsageLimit, r.Failure.Class)

	out, err := json.Marshal(r)
	require.NoError(t, err)
	var want, got map[string]any
	require.NoError(t, json.Unmarshal(raw, &want))
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, want, got)
}

// A completed run must not grow any v3 field on the wire: a server that
// predates failed runs refuses fields it doesn't know, and a refused run
// is dropped, not retried.
func TestCompletedRunCarriesNothingV3Added(t *testing.T) {
	r := loadGolden(t)
	assert.Equal(t, CompletedSchemaVersion, r.SchemaVersion)
	out, err := json.Marshal(r)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.NotContains(t, got, "status")
	assert.NotContains(t, got, "failure")
}

// A failed run can fail before anything about the change is known.
func TestValidate_AcceptsAFailedRunThatKnowsAlmostNothing(t *testing.T) {
	r := &Run{
		SchemaVersion: SchemaVersion, ID: uuid.New(), Kind: KindReview,
		Status:      StatusFailed,
		Failure:     &Failure{Class: FailureConfig, Message: "config: OPENROUTER_API_KEY is required"},
		ToolVersion: ToolVersion{Kind: "unknown", Value: "unknown"},
		StartedAt:   loadGolden(t).StartedAt, FinishedAt: loadGolden(t).StartedAt,
	}
	require.NoError(t, r.Validate())
}

func TestValidate_AcceptsACompletedRunAtEitherVersion(t *testing.T) {
	r := loadGolden(t)
	r.SchemaVersion = SchemaVersion
	require.NoError(t, r.Validate())
}

func TestValidate_RejectsEachFailedRunMalformation(t *testing.T) {
	cases := map[string]struct {
		mutate func(r *Run)
		want   string
	}{
		"failed at v2":             {func(r *Run) { r.SchemaVersion = CompletedSchemaVersion }, "which this run needs"},
		"unknown status":           {func(r *Run) { r.Status = "crashed" }, "status"},
		"no failure":               {func(r *Run) { r.Failure = nil }, "failure is required"},
		"unknown class":            {func(r *Run) { r.Failure.Class = "gremlins" }, "failure.class"},
		"no message":               {func(r *Run) { r.Failure.Message = " " }, "failure.message is required"},
		"oversized message":        {func(r *Run) { r.Failure.Message = strings.Repeat("x", MaxTextBytes+1) }, "failure.message is longer"},
		"oversized host":           {func(r *Run) { r.Failure.Host = strings.Repeat("x", maxShortString+1) }, "failure.host is longer"},
		"a verdict":                {func(r *Run) { r.Verdict = "approve" }, "verdict must be empty"},
		"findings":                 {func(r *Run) { r.Findings = loadGolden(t).Findings }, "findings must be empty"},
		"a diff":                   {func(r *Run) { r.Diff = "diff" }, "diff must be empty on a failed run"},
		"oversized branch":         {func(r *Run) { r.Branch = strings.Repeat("x", maxShortString+1) }, "branch is longer"},
		"local-path repo_remote":   {func(r *Run) { r.RepoRemote = "/home/me/repo" }, "repo_remote"},
		"credentialed repo_remote": {func(r *Run) { r.RepoRemote = "https://ghp_xxxSECRETxxx@github.com/example/widgets.git" }, "credentials in its userinfo"},
		"no tool version":          {func(r *Run) { r.ToolVersion = ToolVersion{} }, "tool_version.kind is required"},
		"bad session part":         {func(r *Run) { r.Sessions[0].Part = 2 }, "is not a part"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := loadFailedGolden(t)
			tc.mutate(r)
			err := r.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestValidate_RejectsAFailureOnACompletedRun(t *testing.T) {
	r := loadGolden(t)
	r.Failure = &Failure{Class: FailureAuth, Message: "m"}
	err := r.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failure must be empty")
}

// A run that used none of v4 is sent exactly as before, so a server that
// predates v4 still reads it; one that did is v4, or it is refused.
func TestMinSchemaVersion(t *testing.T) {
	assert.Equal(t, CompletedSchemaVersion, loadGolden(t).MinSchemaVersion())
	assert.Equal(t, FailedSchemaVersion, loadFailedGolden(t).MinSchemaVersion())

	for name, mutate := range map[string]func(r *Run){
		"a config revision": func(r *Run) { r.ProviderConfigRevision = 3 },
		"a fallback": func(r *Run) {
			r.Provider = "chatgpt"
			r.Fallback = &Fallback{From: "chatgpt", To: "openrouter", Class: FailureUsageLimit}
		},
		"a session model": func(r *Run) { r.Sessions[0].Model = "gpt-5.6-luna" },
	} {
		t.Run(name, func(t *testing.T) {
			for _, r := range []*Run{loadGolden(t), loadFailedGolden(t)} {
				mutate(r)
				assert.Equal(t, 4, r.MinSchemaVersion())
				err := r.Validate()
				require.Error(t, err, "a v4 field at v%d", r.SchemaVersion)
				assert.Contains(t, err.Error(), "schema_version")
				r.SchemaVersion = SchemaVersion
				require.NoError(t, r.Validate())
			}
		})
	}
}

func TestValidate_RejectsEachFallbackMalformation(t *testing.T) {
	cases := map[string]struct {
		mutate func(f *Fallback)
		want   string
	}{
		"no from":         {func(f *Fallback) { f.From = "" }, "fallback.from is required"},
		"not the primary": {func(f *Fallback) { f.From = "openai" }, "must be the run's provider"},
		"no to":           {func(f *Fallback) { f.To = " " }, "fallback.to is required"},
		"same provider":   {func(f *Fallback) { f.To = f.From }, "must differ"},
		"config class":    {func(f *Fallback) { f.Class = FailureConfig }, "fallback.class"},
		"unknown class":   {func(f *Fallback) { f.Class = "gremlins" }, "fallback.class"},
		"oversized host":  {func(f *Fallback) { f.Host = strings.Repeat("x", maxShortString+1) }, "fallback.host is longer"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := loadGolden(t)
			r.SchemaVersion = SchemaVersion
			r.Provider = "chatgpt"
			r.Fallback = &Fallback{From: "chatgpt", To: "openrouter", Class: FailureUsageLimit, Host: "vm-1"}
			require.NoError(t, r.Validate())
			tc.mutate(r.Fallback)
			err := r.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func loadV5Golden(t *testing.T) *Run {
	t.Helper()
	raw, err := os.ReadFile("testdata/run_v5.json")
	require.NoError(t, err)
	var r Run
	require.NoError(t, json.Unmarshal(raw, &r))
	return &r
}

func TestV5GoldenRunIsValidAndRoundTripsEveryJSONField(t *testing.T) {
	raw, err := os.ReadFile("testdata/run_v5.json")
	require.NoError(t, err)
	r := loadV5Golden(t)
	require.NoError(t, r.Validate())
	assert.Equal(t, UsageSchemaVersion, r.MinSchemaVersion())

	out, err := json.Marshal(r)
	require.NoError(t, err)
	var want, got map[string]any
	require.NoError(t, json.Unmarshal(raw, &want))
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, want, got)
}

// A run with no usage must not grow any v5 field on the wire, so a server
// that predates v5 keeps reading it.
func TestARunWithoutUsageCarriesNothingV5Added(t *testing.T) {
	r := loadGolden(t)
	assert.Equal(t, CompletedSchemaVersion, r.MinSchemaVersion())
	out, err := json.Marshal(r)
	require.NoError(t, err)
	for _, field := range []string{"billing_mode", "requests", "cache_read_tokens", "reasoning_tokens", "reported_cost_microdollars", "tools"} {
		assert.NotContains(t, string(out), `"`+field+`"`)
	}
	for i := range r.Sessions {
		assert.Nil(t, r.Sessions[i].Usage(r), "a pre-v5 session has no usage record")
	}
}

func TestMinSchemaVersion_AnyUsageFieldNeedsV5(t *testing.T) {
	cost := int64(1)
	for name, mutate := range map[string]func(s *Session){
		"billing mode": func(s *Session) { s.BillingMode = llmusage.BillingSubscription },
		"requests":     func(s *Session) { s.Requests = 1 },
		"cache reads":  func(s *Session) { s.CacheReadTokens = 1 },
		"reasoning":    func(s *Session) { s.ReasoningTokens = 1 },
		"cost":         func(s *Session) { s.ReportedCostMicros = &cost },
		"tools":        func(s *Session) { s.Tools = []llmusage.ToolUsage{{Tool: "read_file", Calls: 1}} },
	} {
		t.Run(name, func(t *testing.T) {
			// A failed run's sessions can carry usage too: it needs v5 just
			// the same.
			failed := loadFailedGolden(t)
			mutate(&failed.Sessions[0])
			assert.Equal(t, UsageSchemaVersion, failed.MinSchemaVersion())

			r := loadGolden(t)
			mutate(&r.Sessions[0])
			assert.Equal(t, UsageSchemaVersion, r.MinSchemaVersion())
			r.SchemaVersion = 4
			err := r.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "schema_version")
		})
	}
}

func TestSessionUsage_IsARecordInGoLLMUsageTerms(t *testing.T) {
	r := loadV5Golden(t)

	u := r.Sessions[0].Usage(r)
	require.NotNil(t, u)
	assert.Equal(t, "openrouter", u.Provider, "a session naming no provider ran on the run's")
	assert.Equal(t, "openai/gpt-5.6-luna", u.Model)
	assert.Equal(t, llmusage.HarnessSlopReview, u.Harness)
	assert.Equal(t, llmusage.BillingMetered, u.BillingMode)
	assert.Equal(t, int64(4), u.Requests)
	assert.Equal(t, int64(1000), u.InputTokens)
	assert.Equal(t, int64(200), u.CacheReadTokens)
	assert.Equal(t, int64(300), u.OutputTokens)
	assert.Equal(t, int64(120), u.ReasoningTokens)
	require.NotNil(t, u.ReportedCostMicros)
	assert.Equal(t, int64(1830), *u.ReportedCostMicros)
	assert.Nil(t, u.EstimatedCostMicros, "the estimate is the server's to make")
	assert.Equal(t, r.StartedAt, u.StartedAt)
	assert.Equal(t, r.FinishedAt, u.EndedAt)
	assert.Equal(t, r.Sessions[0].Tools, u.Tools, "every tool field is carried across")
	require.NoError(t, u.Validate())

	// A copy: pricing the record must not reach back into the report.
	u.Tools[0].Calls = 99
	*u.ReportedCostMicros = 7
	assert.Equal(t, int64(2), r.Sessions[0].Tools[0].Calls)
	assert.Equal(t, int64(1830), *r.Sessions[0].ReportedCostMicros)

	u = r.Sessions[1].Usage(r)
	assert.Equal(t, "chatgpt", u.Provider)
	assert.Equal(t, "gpt-5.6-luna", u.Model)
	assert.Nil(t, u.ReportedCostMicros, "a subscription reports no cost")
}

// The session's usage is held to go-llm-usage's own limits.
func TestValidate_RejectsEachUsageMalformation(t *testing.T) {
	cases := map[string]struct {
		mutate func(s *Session)
		want   string
	}{
		"usage without a billing mode": {func(s *Session) { s.BillingMode = "" }, "need a billing_mode"},
		"unknown billing mode":         {func(s *Session) { s.BillingMode = "free" }, "billing_mode"},
		"negative requests":            {func(s *Session) { s.Requests = -1 }, "requests"},
		"negative cache reads":         {func(s *Session) { s.CacheReadTokens = -1 }, "cache_read_tokens"},
		"reasoning beyond output":      {func(s *Session) { s.ReasoningTokens = s.OutputTokens + 1 }, "reasoning_tokens"},
		"negative cost": {func(s *Session) {
			c := int64(-1)
			s.ReportedCostMicros = &c
		}, "reported_cost_microdollars"},
		"absurd cost": {func(s *Session) {
			c := llmusage.MaxCostMicros + 1
			s.ReportedCostMicros = &c
		}, "reported_cost_microdollars"},
		"malformed tool name":    {func(s *Session) { s.Tools[0].Tool = "read file; rm -rf" }, "tool"},
		"tool listed twice":      {func(s *Session) { s.Tools[1] = s.Tools[0] }, "listed twice"},
		"tool errors past calls": {func(s *Session) { s.Tools[0].Errors = s.Tools[0].Calls + 1 }, "errors"},
		"malformed command":      {func(s *Session) { s.Tools[0].Command = "git push --force origin" }, "command"},
		"too many tools": {func(s *Session) {
			s.Tools = nil
			for i := 0; i <= llmusage.MaxToolsPerRecord; i++ {
				s.Tools = append(s.Tools, llmusage.ToolUsage{Tool: fmt.Sprintf("t%d", i), Calls: 1})
			}
		}, "tools"},
		"malformed model": {func(s *Session) { s.Model = "a model with spaces" }, "model"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := loadV5Golden(t)
			require.NoError(t, r.Validate())
			tc.mutate(&r.Sessions[0])
			err := r.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "sessions[0]")
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// withFingerprints gives the golden run v6 fingerprints that validate: its
// first session's persona text, hashed.
func withFingerprints(t *testing.T) *Run {
	t.Helper()
	r := loadGolden(t)
	text := "persona text\n"
	h := HashText(text)
	r.Fingerprint, r.SourceHash = HashText("run"), HashText("source")
	r.PersonaTexts = map[string]string{h: text}
	r.Sessions[0].PersonaHash = h
	r.SchemaVersion = FingerprintSchemaVersion
	return r
}

func TestFingerprints_NeedV6AndValidate(t *testing.T) {
	r := withFingerprints(t)
	assert.Equal(t, FingerprintSchemaVersion, r.MinSchemaVersion())
	require.NoError(t, r.Validate())

	r.SchemaVersion = UsageSchemaVersion
	err := r.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema_version")
}

func TestARunWithoutFingerprintsCarriesNothingV6Added(t *testing.T) {
	out, err := json.Marshal(loadGolden(t))
	require.NoError(t, err)
	for _, field := range []string{"fingerprint", "source_hash", "persona_texts", "persona_hash"} {
		assert.NotContains(t, string(out), `"`+field+`"`)
	}
}

func TestValidate_RejectsEachFingerprintMalformation(t *testing.T) {
	cases := map[string]struct {
		mutate func(r *Run)
		want   string
	}{
		"bad fingerprint":        {func(r *Run) { r.Fingerprint = "abc" }, "fingerprint must be"},
		"uppercase source hash":  {func(r *Run) { r.SourceHash = strings.ToUpper(r.SourceHash) }, "source_hash must be"},
		"fingerprint no source":  {func(r *Run) { r.SourceHash = "" }, "fingerprint needs source_hash"},
		"text not its hash":      {func(r *Run) { r.PersonaTexts[r.Sessions[0].PersonaHash] = "tampered" }, "does not hash to its key"},
		"bad text key":           {func(r *Run) { r.PersonaTexts["nope"] = "x" }, "persona_texts key"},
		"oversized text":         {func(r *Run) { big := strings.Repeat("x", MaxTextBytes+1); r.PersonaTexts[HashText(big)] = big }, "is longer than"},
		"session hash malformed": {func(r *Run) { r.Sessions[0].PersonaHash = "zz" }, "persona_hash must be"},
		"session hash no text":   {func(r *Run) { r.Sessions[0].PersonaHash = HashText("other") }, "no text in persona_texts"},
		"too many texts": {func(r *Run) {
			for i := 0; i <= MaxPersonaTexts; i++ {
				r.PersonaTexts[HashText(strconv.Itoa(i))] = strconv.Itoa(i)
			}
		}, "persona_texts has"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := withFingerprints(t)
			c.mutate(r)
			err := r.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}
