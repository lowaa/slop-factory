// Package runreport builds the report.Run slop-review posts to
// the report server (see report/reporter): from a finished orchestrator
// result -- everything the review already knows, plus what only the caller
// can supply: the scope reviewed, the config it ran under, and the tool's
// own version -- or, for a run that failed, from whatever was known when it
// did.
package runreport

import (
	"errors"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-review/internal/config"
	"github.com/lowaa/slop-factory/slop-review/internal/fingerprint"
	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/orchestrator"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
	"github.com/lowaa/slop-factory/slop-review/report"
)

// Input is everything Build needs beyond what the orchestrator result
// itself carries.
type Input struct {
	Result   *orchestrator.Result
	Scope    *gitctx.Scope
	Cfg      *config.Config
	Personas []string
	// Fingerprint is the reviewer's content fingerprint; nil when it could
	// not be computed.
	Fingerprint *fingerprint.Result
	HeadSHA     string
	Dirty       bool
	// Host is the machine the run ran on, reported only when it fell back:
	// a lost login is fixed there.
	Host       string
	StartedAt  time.Time
	FinishedAt time.Time
}

// Build maps a finished review into the report.Run slop-review posts. It
// does not call Validate -- the caller does that once, right before
// sending, so a malformed run is caught at the one place that matters
// rather than on every intermediate construction.
func Build(in Input) *report.Run {
	r := &report.Run{
		ID:   uuid.New(),
		Kind: report.KindReview,

		RepoRemote: reportRemote(in.Cfg, in.Scope),
		Branch:     in.Scope.Branch,
		BaseRef:    in.Scope.BaseRef,
		HeadSHA:    in.HeadSHA,
		HeadAuthor: in.Scope.HeadAuthor,
		Dirty:      in.Dirty,
		ScopePath:  in.Scope.ScopePath,

		ToolVersion: resolveToolVersion(in.Cfg),
		Provider:    in.Cfg.Provider,
		Model:       in.Cfg.Model,
		Personas:    in.Personas,
		Config:      reportConfig(in.Cfg),

		Verdict:    string(in.Result.Review.Verdict),
		Summary:    in.Result.Review.Summary,
		Incomplete: in.Result.Review.Incomplete,
		Warnings:   in.Result.Review.Warnings,

		Sessions: buildSessions(in.Result.Sessions, in.Cfg),
		Findings: buildFindings(in.Result.Review.Findings),

		StartedAt:  in.StartedAt,
		FinishedAt: in.FinishedAt,
	}

	// A clean run's change is recoverable from BaseRef/HeadSHA; a dirty
	// one's working-tree edits are not, so only a dirty run carries the
	// diff itself (see report.Run.Diff's own doc comment).
	if in.Dirty {
		r.Diff = in.Scope.Diff()
	}

	if isBenchRun(in.Cfg) {
		r.Kind = report.KindBench
		r.Bench = &report.Bench{Case: in.Cfg.BenchCase, Variant: in.Cfg.BenchVariant, Run: in.Cfg.BenchRun}
	}

	setFingerprint(r, in.Fingerprint)
	setProviderConfig(r, in.Cfg, in.Result.FellBack, in.Host)
	return r
}

// setFingerprint records the reviewer's content fingerprint, and on each
// session the hash of the persona it ran, with the text of those personas
// so the server can keep it. Must come before setProviderConfig, which
// settles the schema version from what the run carries.
func setFingerprint(r *report.Run, fp *fingerprint.Result) {
	if fp == nil {
		return
	}
	r.Fingerprint, r.SourceHash = fp.Run, fp.Source
	for i := range r.Sessions {
		p, ok := fp.Personas[r.Sessions[i].Persona]
		if !ok {
			continue
		}
		r.Sessions[i].PersonaHash = p.Hash
		if r.PersonaTexts == nil {
			r.PersonaTexts = map[string]string{}
		}
		r.PersonaTexts[p.Hash] = p.Text
	}
}

// setProviderConfig records the central provider config a run ran under,
// and whether it fell back, then settles the schema version: a run set up
// from local settings uses nothing v4 added, and goes out exactly as it
// did before, to a server that may not know v4 yet. A run whose sessions
// carry their usage is v5 whatever else it has.
func setProviderConfig(r *report.Run, cfg *config.Config, fellBack *orchestrator.FellBack, host string) {
	r.ProviderConfigRevision = cfg.ProviderConfigRevision
	if fellBack != nil {
		r.Fallback = &report.Fallback{From: fellBack.From, To: fellBack.To, Class: report.FailureClass(fellBack.Class), Host: host}
	}
	r.SchemaVersion = r.MinSchemaVersion()
}

func reportConfig(cfg *config.Config) report.Config {
	return report.Config{
		MaxTurns:           cfg.MaxTurns,
		MaxDiffBytes:       cfg.MaxDiffBytes,
		ChunkDiffBytes:     cfg.ChunkDiffBytes,
		ContextBudgetBytes: cfg.ContextBudgetBytes,
		Parallelism:        cfg.Parallelism,
		SeamPass:           cfg.SeamPass,
		AllowSecrets:       cfg.SecretsMayBeQuoted(),
		ReasoningEffort:    cfg.ReasoningEffort,
	}
}

// FailedInput is what is known about a run that failed before it produced a
// review. Scope is nil when the failure came before the change was
// resolved; Personas is empty when it came before they were loaded.
type FailedInput struct {
	Err      error
	Class    report.FailureClass
	Cfg      *config.Config
	Scope    *gitctx.Scope
	Personas []string
	// Fingerprint is nil when the run failed before it was computed.
	Fingerprint *fingerprint.Result
	Host        string
	// FellBack is set when the run had moved to its fallback provider
	// before it failed.
	FellBack   *orchestrator.FellBack
	StartedAt  time.Time
	FinishedAt time.Time
}

// maxFailureMessageBytes keeps a failure's message inside the contract's
// bound however many sessions' errors it joins.
const maxFailureMessageBytes = report.MaxTextBytes

// BuildFailed maps a failed run into the report.Run slop-review posts, so
// the server hears about the runs that matter most: the ones that never
// produced a review. Like Build, it does not call Validate.
func BuildFailed(in *FailedInput) *report.Run {
	r := &report.Run{
		ID:     uuid.New(),
		Kind:   report.KindReview,
		Status: report.StatusFailed,
		Failure: &report.Failure{
			Class:   in.Class,
			Message: failureMessage(in.Err, in.Cfg),
			Host:    in.Host,
		},
		ToolVersion: resolveToolVersion(in.Cfg),
		Provider:    in.Cfg.Provider,
		Model:       in.Cfg.Model,
		Personas:    in.Personas,
		Config:      reportConfig(in.Cfg),
		StartedAt:   in.StartedAt,
		FinishedAt:  in.FinishedAt,
	}
	scope := in.Scope
	if scope == nil {
		scope = &gitctx.Scope{}
	}
	r.Branch, r.BaseRef, r.HeadSHA, r.HeadAuthor = scope.Branch, scope.BaseRef, scope.HeadSHA, scope.HeadAuthor
	r.Dirty, r.ScopePath = scope.Dirty, scope.ScopePath
	// A remote the server couldn't key a repo by would get the whole report
	// refused, and a failure is worth more reported without its repo than
	// not at all.
	if remote := reportRemote(in.Cfg, scope); remote != "" {
		if _, err := report.RemoteKey(remote); err == nil {
			r.RepoRemote = remote
		}
	}
	if isBenchRun(in.Cfg) {
		r.Kind = report.KindBench
		r.Bench = &report.Bench{Case: in.Cfg.BenchCase, Variant: in.Cfg.BenchVariant, Run: in.Cfg.BenchRun}
	}
	setFingerprint(r, in.Fingerprint)
	setProviderConfig(r, in.Cfg, in.FellBack, in.Host)
	return r
}

// urlUserinfo matches the userinfo of a URL written into an error: the SDKs
// quote the request URL, and a base URL or remote can carry credentials.
var urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@]+@`)

// failureMessage is err's text, scrubbed of the credentials this process
// holds and of any URL's userinfo -- the providers already keep their own
// out of their errors, but this text leaves the machine, so it is checked
// once more at the edge -- and cut to the contract's bound.
func failureMessage(err error, cfg *config.Config) string {
	msg := err.Error()
	for _, secret := range []string{cfg.APIKey, cfg.ReportToken} {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[redacted]")
		}
	}
	msg = urlUserinfo.ReplaceAllString(msg, "${1}[redacted]@")
	if len(msg) > maxFailureMessageBytes {
		msg = strings.ToValidUTF8(msg[:maxFailureMessageBytes-len(truncated)], "") + truncated
	}
	return msg
}

const truncated = " [truncated]"

// Classify maps the error a run failed with to its failure class, by the
// provider errors it wraps, or to fallback when it wraps none -- which is
// what the stage that failed says it most likely was. Where a run's
// sessions failed in different ways, the class that needs a person soonest
// wins.
func Classify(err error, fallback report.FailureClass) report.FailureClass {
	for _, c := range []struct {
		class report.FailureClass
		errs  []error
	}{
		{report.FailureUsageLimit, []error{llm.ErrUsageLimit}},
		{report.FailureCredits, []error{llm.ErrCredits}},
		{report.FailureAuth, []error{llm.ErrAuth, llm.ErrKeyRejected}},
		{report.FailureBackend, []error{llm.ErrBackend, llm.ErrRequest}},
	} {
		for _, target := range c.errs {
			if errors.Is(err, target) {
				return c.class
			}
		}
	}
	return fallback
}

// reportRemote is the git remote URL a report is filed under: --report-repo
// if the caller set one (for a repo whose configured remote isn't the
// shared identity -- a personal fork, say), otherwise whatever
// --report-remote resolved to when the scope was built (see
// gitctx.ScopeOptions.RemoteName). Empty when neither names one -- Validate
// rejects that, since report.RemoteKey needs something to key the repo by.
//
// Redacted before it leaves this function: either source can carry a
// credential (an HTTPS remote with an embedded token), and nothing
// downstream -- the spool, the server, another user's browser -- should
// ever see it.
func reportRemote(cfg *config.Config, scope *gitctx.Scope) string {
	if cfg.ReportRepo != "" {
		return report.RedactRemote(cfg.ReportRepo)
	}
	return report.RedactRemote(scope.RemoteURL)
}

// isBenchRun reports whether every one of --bench-case/--bench-variant/
// --bench-run was supplied -- slop-review-bench's ExecReviewer always
// passes all three together, so a partial set (say, --bench-case alone)
// is not a bench run, just an unused flag.
func isBenchRun(cfg *config.Config) bool {
	return cfg.BenchCase != "" && cfg.BenchVariant != "" && cfg.BenchRun > 0
}

// unknownToolVersion is reported when neither review.sh nor the build says
// which version ran -- `go run` records no VCS info. An honest "unknown"
// bucket keeps the run; refusing to report it would lose the run entirely.
var unknownToolVersion = report.ToolVersion{Kind: "unknown", Value: "unknown"}

// resolveToolVersion prefers the tool version review.sh passes explicitly
// (its own checkout's git SHA, plus whether that checkout was dirty --
// see config.Config.ToolVersion/ToolVersionDirty), since that's accurate
// for both `go run` and a built binary. Lacking that -- slop-review invoked
// directly, without review.sh -- it falls back to the Go build's own
// embedded VCS info, which `go build` (not `go run`) records automatically.
func resolveToolVersion(cfg *config.Config) report.ToolVersion {
	if cfg.ToolVersion != "" {
		return report.ToolVersion{
			Kind:       "git_sha",
			Value:      cfg.ToolVersion,
			Attributes: map[string]string{"dirty": strconv.FormatBool(cfg.ToolVersionDirty)},
		}
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownToolVersion
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if revision == "" {
		return unknownToolVersion
	}
	attrs := map[string]string{}
	if modified != "" {
		attrs["dirty"] = modified
	}
	return report.ToolVersion{Kind: "git_sha", Value: revision, Attributes: attrs}
}

// buildSessions maps the sessions across. Which provider and model each ran
// on is sent only under a central config (v4): without one, every session
// ran on the run's own provider, which the report already says.
func buildSessions(sessions []orchestrator.SessionReport, cfg *config.Config) []report.Session {
	out := make([]report.Session, len(sessions))
	for i, s := range sessions {
		var provider, model string
		if cfg.ProviderConfigRevision != 0 {
			provider, model = s.Provider, s.Model
		}
		out[i] = report.Session{
			Provider: provider,
			Model:    model,

			Persona:  s.Persona,
			Part:     s.Part,
			Parts:    s.Parts,
			Files:    s.Files,
			Verdict:  string(s.Verdict),
			Summary:  s.Summary,
			Findings: s.Findings,

			Incomplete: s.Incomplete,
			Error:      s.Error,

			Turns:         s.Turns,
			Elisions:      s.Elisions,
			UnseenFiles:   s.UnseenFiles,
			InputTokens:   s.InputTokens,
			OutputTokens:  s.OutputTokens,
			DurationMilli: s.DurationMilli,

			// The session's LLM usage (v5). A session that never started
			// ran on no route, so it has no billing mode and sends none.
			BillingMode:        s.BillingMode,
			Requests:           s.Requests,
			CacheReadTokens:    s.CacheReadTokens,
			ReasoningTokens:    s.ReasoningTokens,
			ReportedCostMicros: s.ReportedCostMicros,
			Tools:              s.Tools,
		}
	}
	return out
}

func buildFindings(findings []review.Finding) []report.Finding {
	out := make([]report.Finding, len(findings))
	for i, f := range findings {
		out[i] = report.Finding{
			ID:       f.ID,
			File:     f.File,
			Line:     f.Line,
			Severity: string(f.Severity),
			Summary:  f.Summary,
			Detail:   f.Detail,
			Personas: f.Personas,
		}
	}
	return out
}
