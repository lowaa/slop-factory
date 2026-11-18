// See doc.go for the package comment.
//
// A run's IDs -- its own and each finding's -- are chosen by the client, as
// random UUIDs, before anything is sent. That makes a post idempotent (a
// spooled run retried after a lost response is recognised, not duplicated)
// and means a finding has a stable ID the moment the review prints it, even
// when the server is unreachable and the run is still waiting in the spool.

package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
)

// SchemaVersion is bumped on any change a server must know about to read a
// report correctly. The server refuses versions it doesn't know rather than
// guessing.
//
// v2: repo_remote must never carry credentials in its userinfo (see
// Run.Validate) -- a server on v1 would accept and store one.
// v3: a run can have failed (Status, Failure). Only a failed run needs v3:
// a completed run carries nothing v3 added, so it is still sent as
// CompletedSchemaVersion, which a server that predates failed runs reads --
// the client and server are deployed separately, and a report the server
// refuses is never retried.
// v4: a run can say which central provider config it ran under
// (ProviderConfigRevision), which provider and model each session ran on,
// and that it fell back to another provider (Fallback). Likewise sent only
// by a run that has one of these; see MinSchemaVersion.
// v5: a session can carry its LLM usage (Session.BillingMode and the fields
// beside it), in go-llm-usage's Record terms -- which changes what
// input_tokens means: from v5 it excludes cache_read_tokens, where before
// it was the whole prompt (no earlier version reports cached tokens, so a
// stored input_tokens means the same either way). Sent only by a session
// that has usage.
// v6: a run carries content fingerprints of the reviewer that ran
// (Fingerprint, SourceHash, and each session's PersonaHash with the text
// behind it in PersonaTexts). Sent only by a run that has one.
const SchemaVersion = 6

// FingerprintSchemaVersion is the oldest version a run with fingerprints is
// sent as.
const FingerprintSchemaVersion = 6

// UsageSchemaVersion is the oldest version a session's usage is sent as.
const UsageSchemaVersion = 5

// CompletedSchemaVersion is the oldest version a completed run is sent as.
const CompletedSchemaVersion = 2

// FailedSchemaVersion is the oldest version a failed run is sent as.
const FailedSchemaVersion = 3

// IngestPath is where a report is posted, relative to the server's base URL.
const IngestPath = "/api/v1/ingest/runs"

type Kind string

// Status is how a run ended. The zero value is a completed run, so a
// completed run's JSON is unchanged from v2.
type Status string

const (
	StatusCompleted Status = ""
	StatusFailed    Status = "failed"
)

// FailureClass says why a run failed, in terms of what someone has to do
// about it. A report server may alert on some of them; the rest are
// recorded only.
type FailureClass string

const (
	// FailureUsageLimit: a subscription's usage limit is reached.
	FailureUsageLimit FailureClass = "usage_limit"
	// FailureAuth: the provider refused the credential, or there was none
	// (a lost login, a rejected API key).
	FailureAuth FailureClass = "auth"
	// FailureCredits: a pay-as-you-go account is out of credits.
	FailureCredits FailureClass = "credits"
	// FailureBackend: the provider or the connection to it failed, after
	// retries.
	FailureBackend FailureClass = "backend"
	// FailureConfig: the run was set up wrongly (a bad flag, a missing key,
	// not a git repository) -- a local problem.
	FailureConfig FailureClass = "config"
	// FailureOther: anything else, e.g. every session ran out of turns
	// without a review.
	FailureOther FailureClass = "other"
)

var validFailureClasses = map[FailureClass]bool{
	FailureUsageLimit: true, FailureAuth: true, FailureCredits: true,
	FailureBackend: true, FailureConfig: true, FailureOther: true,
}

// Failure is why a failed run failed.
type Failure struct {
	Class FailureClass `json:"class"`
	// Message is the error the run failed with, which names the fix.
	Message string `json:"message"`
	// Host is the machine the run failed on: a lost login is fixed there.
	// Informational, like HeadAuthor.
	Host string `json:"host,omitempty"`
}

// Fallback says a run moved from its primary provider to the fallback the
// tenant configured, and why. Which sessions ran where is on each Session.
type Fallback struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Class is the primary's failure that triggered it.
	Class FailureClass `json:"class"`
	// Host is the machine the run fell back on: a lost login is fixed
	// there. Informational, like Failure.Host.
	Host string `json:"host,omitempty"`
}

const (
	KindReview Kind = "review"
	KindBench  Kind = "bench"
)

// Run is one slop-review invocation: everything reviewed, how, and what came
// of it.
type Run struct {
	SchemaVersion int       `json:"schema_version"`
	ID            uuid.UUID `json:"id"`
	Kind          Kind      `json:"kind"`
	// Status and Failure are set only on a failed run, which failed before
	// or while reviewing -- so it carries whatever was known by then, and
	// no review: a failed run's repo, branch, commits, personas and
	// sessions may all be missing.
	Status  Status   `json:"status,omitempty"`
	Failure *Failure `json:"failure,omitempty"`

	// RepoRemote is the reviewed repository's git remote URL, as
	// configured. The server files the run under the repo it names (see
	// RemoteKey), registering it on first sight.
	RepoRemote string `json:"repo_remote"`
	Branch     string `json:"branch"`
	BaseRef    string `json:"base_ref"`
	HeadSHA    string `json:"head_sha"`
	// HeadAuthor is the head commit's author ("Name <email>"), as git
	// records it: informational, since the client can write anything here.
	// Who ran the review comes from the credential, not from this.
	HeadAuthor string `json:"head_author,omitempty"`
	Dirty      bool   `json:"dirty"`
	ScopePath  string `json:"scope_path,omitempty"`
	// Diff is the redacted diff reviewed, sent only when Dirty: a clean
	// run's change is recoverable from BaseRef and HeadSHA, a dirty one's
	// is not.
	Diff string `json:"diff,omitempty"`

	ToolVersion ToolVersion `json:"tool_version"`
	// Fingerprint (v6) is a hash of what the reviewer ran with: its source
	// (SourceHash) and every persona in its library. Unlike ToolVersion's
	// git SHA it is the same for the same content in any checkout, so runs
	// group by it. SourceHash covers the Go source and the text it embeds.
	// Both are hex SHA-256; each is empty when the reviewer could not read
	// its own source, and Fingerprint is set only with SourceHash.
	Fingerprint string `json:"fingerprint,omitempty"`
	SourceHash  string `json:"source_hash,omitempty"`
	// PersonaTexts (v6) holds the text of each persona the sessions ran,
	// by the hash that Session.PersonaHash names: the server keeps each
	// text once, so two versions can be compared without git. A text must
	// hash to its key.
	PersonaTexts map[string]string `json:"persona_texts,omitempty"`
	Provider     string            `json:"provider"`
	Model        string            `json:"model"`
	Personas     []string          `json:"personas"`
	Config       Config            `json:"config"`
	// ProviderConfigRevision is the revision of the tenant's central
	// provider config the run was set up from (see package
	// providerconfig); zero when it used local settings. Provider and Model
	// above are the config's primary either way.
	ProviderConfigRevision int64 `json:"provider_config_revision,omitempty"`
	// Fallback is set when some or all of the run's sessions ran on the
	// config's fallback provider rather than the primary.
	Fallback *Fallback `json:"fallback,omitempty"`

	Verdict    string   `json:"verdict"`
	Summary    string   `json:"summary"`
	Incomplete bool     `json:"incomplete"`
	Warnings   []string `json:"warnings,omitempty"`

	Sessions []Session `json:"sessions"`
	// Findings are the merged findings: what the review reported.
	Findings []Finding `json:"findings"`

	// Bench is set only for Kind bench.
	Bench *Bench `json:"bench,omitempty"`

	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// ToolVersion identifies the build of slop-review that ran. A kind/value
// pair rather than a git SHA field, so a container image tag can replace
// the SHA later without the contract changing; Attributes carries whatever
// else is known (the SHA an image was built from, say).
type ToolVersion struct {
	Kind       string            `json:"kind"`
	Value      string            `json:"value"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Config is the orchestration settings the run used -- what a comparison
// between runs needs to know changed.
type Config struct {
	MaxTurns           int  `json:"max_turns"`
	MaxDiffBytes       int  `json:"max_diff_bytes"`
	ChunkDiffBytes     int  `json:"chunk_diff_bytes"`
	ContextBudgetBytes int  `json:"context_budget_bytes"`
	Parallelism        int  `json:"parallelism"`
	SeamPass           bool `json:"seam_pass"`
	// ReasoningEffort is the effort the provider was asked for, when it
	// takes one as a parameter; empty means the model's default (and is
	// all OpenRouter ever reports: it selects effort by model slug).
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// AllowSecrets reports that secret-shaped files were shown to the model
	// unredacted. Reported so the server can refuse to store such a run:
	// its findings and diff may quote secrets.
	AllowSecrets bool `json:"allow_secrets"`
}

// Session is one review session: a persona over one part of the change.
type Session struct {
	Persona string `json:"persona"`
	// PersonaHash (v6) is the hash of the persona file the session ran
	// with: hex SHA-256 of its text, which Run.PersonaTexts holds.
	PersonaHash string   `json:"persona_hash,omitempty"`
	Part        int      `json:"part"`
	Parts       int      `json:"parts"`
	Files       []string `json:"files"`
	Verdict     string   `json:"verdict,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Findings    int      `json:"findings"`
	Incomplete  bool     `json:"incomplete,omitempty"`
	Error       string   `json:"error,omitempty"`
	// Provider and Model are what this session ran on, when the run was
	// set up from a central config (v4): with a fallback, not every
	// session ran on the run's own Provider.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	Turns         int      `json:"turns"`
	Elisions      int      `json:"elisions"`
	UnseenFiles   []string `json:"unseen_files,omitempty"`
	InputTokens   int64    `json:"input_tokens"`
	OutputTokens  int64    `json:"output_tokens"`
	DurationMilli int64    `json:"duration_ms"`

	// The session's LLM usage (v5), present exactly when BillingMode is
	// set, and named as go-llm-usage's Record names it: InputTokens above
	// then excludes CacheReadTokens, and ReasoningTokens is part of
	// OutputTokens, never in addition to it. See Usage. Cache writes have
	// no field of their own: the OpenAI models slop-review runs on report
	// none, so a provider that does report them has them counted (and
	// priced) as input.
	BillingMode llmusage.BillingMode `json:"billing_mode,omitempty"`
	// Requests counts every request sent to the provider, retries and
	// failed attempts included -- unlike Turns, which counts conversation
	// turns.
	Requests        int64 `json:"requests,omitempty"`
	CacheReadTokens int64 `json:"cache_read_tokens,omitempty"`
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
	// ReportedCostMicros is what the provider said it charged, in
	// micro-dollars, when it says (OpenRouter); never inferred. The server
	// estimates cost from the tokens for every session either way.
	ReportedCostMicros *int64 `json:"reported_cost_microdollars,omitempty"`
	// Tools are the session's tool calls by raw tool name; the server
	// categorises them, so Category is left empty.
	Tools []llmusage.ToolUsage `json:"tools,omitempty"`
}

// HasUsage reports whether the session carries any v5 usage field.
func (s *Session) HasUsage() bool {
	return s.BillingMode != "" || s.Requests != 0 || s.CacheReadTokens != 0 || s.ReasoningTokens != 0 ||
		s.ReportedCostMicros != nil || len(s.Tools) > 0
}

// Usage returns the session's LLM usage as a go-llm-usage Record, for r,
// the run it belongs to -- nil when the session has none (one sent before
// v5). The provider and model are the session's, or the run's when the
// session doesn't name its own (see Provider). The record covers the run's
// time span: a session's own start isn't reported.
func (s *Session) Usage(r *Run) *llmusage.Record {
	if s.BillingMode == "" {
		return nil
	}
	provider, model := s.Provider, s.Model
	if provider == "" {
		provider = r.Provider
	}
	if model == "" {
		model = r.Model
	}
	rec := &llmusage.Record{
		SchemaVersion: llmusage.SchemaVersion,
		Provider:      provider,
		Model:         model,
		Harness:       llmusage.HarnessSlopReview,
		BillingMode:   s.BillingMode,

		Requests:        s.Requests,
		InputTokens:     s.InputTokens,
		OutputTokens:    s.OutputTokens,
		CacheReadTokens: s.CacheReadTokens,
		ReasoningTokens: s.ReasoningTokens,

		ReportedCostMicros: s.ReportedCostMicros,
		StartedAt:          r.StartedAt,
		EndedAt:            r.FinishedAt,
		Tools:              s.Tools,
	}
	return rec.Clone()
}

type Finding struct {
	ID       uuid.UUID `json:"id"`
	File     string    `json:"file"`
	Line     int       `json:"line,omitempty"`
	Severity string    `json:"severity"`
	Summary  string    `json:"summary"`
	Detail   string    `json:"detail"`
	Personas []string  `json:"personas"`
}

// Bench identifies which benchmark case and variant a bench run scored.
type Bench struct {
	Case    string `json:"case"`
	Variant string `json:"variant"`
	Run     int    `json:"run"`
}

// Accepted is the server's answer to a post. Created is false when the run
// was already recorded -- a retry -- in which case nothing was changed.
type Accepted struct {
	RunID   uuid.UUID `json:"run_id"`
	Created bool      `json:"created"`
	URL     string    `json:"url"`
}

// Limits bound what a server stores from one report. Enforced by Validate
// so the client finds out before sending, and again by the server, which
// must never trust that the client ran it.
const (
	// MaxReportBytes bounds a whole report as sent: compact JSON, as
	// encoding/json writes it. The server's body limit is this same value,
	// so any report that passes Validate can be posted -- a report the
	// server would refuse with 413 would otherwise be lost, since the
	// client doesn't retry what the server rejects.
	MaxReportBytes = 8 << 20
	MaxDiffBytes   = 2 << 20
	MaxSessions    = 500
	MaxFindings    = 2000
	MaxTextBytes   = 64 << 10
	MaxWarnings    = 200
	maxShortString = 1024
)

var (
	validVerdicts   = map[string]bool{"approve": true, "approve_with_comments": true, "request_changes": true, "incomplete": true}
	validSeverities = map[string]bool{"blocker": true, "major": true, "minor": true, "nit": true}
)

// Validate reports every way r is malformed, joined. It checks shape and
// bounds, not truth: whether the findings are right is for labelling.
func (r *Run) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	failed := r.Status == StatusFailed
	if r.Status != StatusCompleted && !failed {
		add("status %q must be empty (completed) or %q", r.Status, StatusFailed)
	} else if least := r.MinSchemaVersion(); r.SchemaVersion < least || r.SchemaVersion > SchemaVersion {
		add("schema_version %d is not between %d, which this run needs, and %d", r.SchemaVersion, least, SchemaVersion)
	}
	r.validateFingerprints(add)
	if r.ProviderConfigRevision < 0 {
		add("provider_config_revision must not be negative")
	}
	if f := r.Fallback; f != nil {
		for name, v := range map[string]string{"fallback.from": f.From, "fallback.to": f.To} {
			if strings.TrimSpace(v) == "" {
				add("%s is required", name)
			} else if len(v) > maxShortString {
				add("%s is longer than %d bytes", name, maxShortString)
			}
		}
		if f.From == f.To {
			add("fallback.to must differ from fallback.from")
		}
		if f.From != r.Provider {
			add("fallback.from %q must be the run's provider %q: the primary is what it fell back from", f.From, r.Provider)
		}
		if !validFailureClasses[f.Class] || f.Class == FailureConfig || f.Class == FailureOther {
			add("fallback.class %q is not a provider failure class", f.Class)
		}
		if len(f.Host) > maxShortString {
			add("fallback.host is longer than %d bytes", maxShortString)
		}
	}
	if r.ID == uuid.Nil {
		add("id is required")
	}
	if r.Kind != KindReview && r.Kind != KindBench {
		add("kind %q must be %q or %q", r.Kind, KindReview, KindBench)
	}
	if (r.Kind == KindBench) != (r.Bench != nil) {
		add("bench must be set exactly when kind is %q", KindBench)
	}
	if r.Bench != nil && (strings.TrimSpace(r.Bench.Case) == "" || strings.TrimSpace(r.Bench.Variant) == "" || r.Bench.Run < 1) {
		add("bench needs a case, a variant and a run number of at least 1")
	}

	// What a completed run must have, a failed one may lack: it can fail
	// before the provider, the scope or the personas are known.
	short := map[string]string{
		"tool_version.kind": r.ToolVersion.Kind, "tool_version.value": r.ToolVersion.Value,
	}
	optional := map[string]string{"head_author": r.HeadAuthor}
	for name, v := range map[string]string{
		"branch": r.Branch, "base_ref": r.BaseRef, "head_sha": r.HeadSHA, "provider": r.Provider, "model": r.Model,
	} {
		if failed {
			optional[name] = v
		} else {
			short[name] = v
		}
	}
	for name, v := range short {
		if strings.TrimSpace(v) == "" {
			add("%s is required", name)
		} else if len(v) > maxShortString {
			add("%s is longer than %d bytes", name, maxShortString)
		}
	}
	for name, v := range optional {
		if len(v) > maxShortString {
			add("%s is longer than %d bytes", name, maxShortString)
		}
	}
	if !failed || r.RepoRemote != "" {
		if _, err := RemoteKey(r.RepoRemote); err != nil {
			add("repo_remote: %v", err)
		}
	}
	// Checked independently of RemoteKey above: a credentialed remote can
	// still resolve to a valid key, so that check alone would let it
	// through. This is defence at the boundary -- the server validates with
	// this same package, so a client bug that forgets to redact still can't
	// get a credential stored.
	if raw := strings.TrimSpace(r.RepoRemote); strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.User != nil {
			add("repo_remote carries credentials in its userinfo; send it without them")
		}
	}
	if len(r.Summary) > MaxTextBytes {
		add("summary is longer than %d bytes", MaxTextBytes)
	}
	if failed {
		r.validateFailure(add)
	} else {
		r.validateReview(add)
	}
	if len(r.Diff) > MaxDiffBytes {
		add("diff is longer than %d bytes", MaxDiffBytes)
	}
	if len(r.Warnings) > MaxWarnings {
		add("more than %d warnings", MaxWarnings)
	}
	if len(r.Sessions) > MaxSessions {
		add("sessions must number at most %d", MaxSessions)
	}
	for i := range r.Sessions {
		s := &r.Sessions[i]
		if strings.TrimSpace(s.Persona) == "" {
			add("sessions[%d]: persona is required", i)
		}
		if s.Part < 1 || s.Parts < s.Part {
			add("sessions[%d]: part %d of %d is not a part", i, s.Part, s.Parts)
		}
		if s.Verdict != "" && !validVerdicts[s.Verdict] {
			add("sessions[%d]: verdict %q is not a known verdict", i, s.Verdict)
		}
		if len(s.Provider) > maxShortString || len(s.Model) > maxShortString {
			add("sessions[%d]: provider or model is longer than %d bytes", i, maxShortString)
		}
		if u := s.Usage(r); u != nil {
			// go-llm-usage's own limits, so what the server prices and
			// counts is exactly what it would accept from any other app.
			if err := u.Validate(); err != nil {
				add("sessions[%d]: usage: %v", i, err)
			}
		} else if s.HasUsage() {
			add("sessions[%d]: usage fields need a billing_mode", i)
		}
	}
	if len(r.Findings) > MaxFindings {
		add("more than %d findings", MaxFindings)
	}
	seen := map[uuid.UUID]bool{}
	for i := range r.Findings {
		f := &r.Findings[i]
		if f.ID == uuid.Nil {
			add("findings[%d]: id is required", i)
		} else if seen[f.ID] {
			add("findings[%d]: id %s is repeated", i, f.ID)
		}
		seen[f.ID] = true
		if strings.TrimSpace(f.File) == "" {
			add("findings[%d]: file is required", i)
		}
		if f.Line < 0 {
			add("findings[%d]: line must not be negative", i)
		}
		if !validSeverities[f.Severity] {
			add("findings[%d]: severity %q is not a known severity", i, f.Severity)
		}
		if strings.TrimSpace(f.Summary) == "" {
			add("findings[%d]: summary is required", i)
		}
		if len(f.Summary) > MaxTextBytes || len(f.Detail) > MaxTextBytes {
			add("findings[%d]: summary or detail is longer than %d bytes", i, MaxTextBytes)
		}
	}
	if r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
		add("started_at and finished_at must be set, and finish after start")
	}
	// Measured last, and only if everything else passed: each field's own
	// bound keeps this from being expensive, and a report already wrong in
	// shape needs fixing before its size matters.
	if len(errs) == 0 {
		if raw, err := json.Marshal(r); err != nil {
			add("encode: %v", err)
		} else if len(raw) > MaxReportBytes {
			add("the report is %d bytes, over the %d-byte limit", len(raw), MaxReportBytes)
		}
	}
	return errors.Join(errs...)
}

// MinSchemaVersion is the oldest schema version that can carry r: the
// version it is sent as, so a run using nothing a newer version added reads
// the same to a server that predates it.
func (r *Run) MinSchemaVersion() int {
	if r.Fingerprint != "" || r.SourceHash != "" || len(r.PersonaTexts) > 0 || slices.ContainsFunc(r.Sessions, func(s Session) bool { return s.PersonaHash != "" }) {
		return FingerprintSchemaVersion
	}
	if slices.ContainsFunc(r.Sessions, func(s Session) bool { return s.HasUsage() }) {
		return UsageSchemaVersion
	}
	if r.ProviderConfigRevision != 0 || r.Fallback != nil || slices.ContainsFunc(r.Sessions, func(s Session) bool {
		return s.Provider != "" || s.Model != ""
	}) {
		return 4
	}
	if r.Status == StatusFailed {
		return FailedSchemaVersion
	}
	return CompletedSchemaVersion
}

// validateReview checks what only a completed run has: a review.
func (r *Run) validateReview(add func(string, ...any)) {
	if r.Failure != nil {
		add("failure must be empty unless status is %q", StatusFailed)
	}
	if !validVerdicts[r.Verdict] {
		add("verdict %q is not a known verdict", r.Verdict)
	}
	if len(r.Personas) == 0 {
		add("personas is required")
	}
	if r.Dirty && r.Diff == "" {
		add("diff is required when dirty")
	}
	if !r.Dirty && r.Diff != "" {
		add("diff must be empty unless dirty: a clean run's change is its commits")
	}
	if len(r.Sessions) == 0 {
		add("sessions must number at least 1")
	}
}

// validateFailure checks a failed run: why it failed, and that it claims
// no review.
func (r *Run) validateFailure(add func(string, ...any)) {
	f := r.Failure
	switch {
	case f == nil:
		add("failure is required when status is %q", StatusFailed)
	default:
		if !validFailureClasses[f.Class] {
			add("failure.class %q is not a known failure class", f.Class)
		}
		if strings.TrimSpace(f.Message) == "" {
			add("failure.message is required")
		} else if len(f.Message) > MaxTextBytes {
			add("failure.message is longer than %d bytes", MaxTextBytes)
		}
		if len(f.Host) > maxShortString {
			add("failure.host is longer than %d bytes", maxShortString)
		}
	}
	if r.Verdict != "" {
		add("verdict must be empty on a failed run")
	}
	if len(r.Findings) > 0 {
		add("findings must be empty on a failed run")
	}
	// Nothing was reviewed, so there's no change worth keeping a copy of.
	if r.Diff != "" {
		add("diff must be empty on a failed run")
	}
}

// MaxPersonaTexts bounds the persona texts of one run: the library is a
// handful of files.
const MaxPersonaTexts = 64

// validateFingerprints checks the v6 fields. A persona text must hash to its
// key, so a stored text can be trusted to be what its hash says.
func (r *Run) validateFingerprints(add func(string, ...any)) {
	for name, h := range map[string]string{"fingerprint": r.Fingerprint, "source_hash": r.SourceHash} {
		if h != "" && !IsHash(h) {
			add("%s must be a hex SHA-256", name)
		}
	}
	if r.Fingerprint != "" && r.SourceHash == "" {
		add("fingerprint needs source_hash")
	}
	if len(r.PersonaTexts) > MaxPersonaTexts {
		add("persona_texts has %d entries, over the limit of %d", len(r.PersonaTexts), MaxPersonaTexts)
		return
	}
	for h, text := range r.PersonaTexts {
		switch {
		case !IsHash(h):
			add("persona_texts key %q must be a hex SHA-256", h)
		case len(text) > MaxTextBytes:
			add("persona_texts[%s] is longer than %d bytes", h, MaxTextBytes)
		case HashText(text) != h:
			add("persona_texts[%s] does not hash to its key", h)
		}
	}
	for i := range r.Sessions {
		h := r.Sessions[i].PersonaHash
		if h == "" {
			continue
		}
		if !IsHash(h) {
			add("sessions[%d].persona_hash must be a hex SHA-256", i)
		} else if _, ok := r.PersonaTexts[h]; !ok {
			add("sessions[%d].persona_hash has no text in persona_texts", i)
		}
	}
}

// HashText is the hash a persona's text is known by: hex SHA-256.
func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// IsHash reports whether s is a lowercase hex SHA-256.
func IsHash(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
