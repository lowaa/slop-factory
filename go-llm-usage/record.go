package llmusage

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// SchemaVersion is the Record wire format's version. A receiver rejects a
// version it does not know rather than guessing at its fields.
const SchemaVersion = 1

// BillingMode says whether a record's cost is money spent or an
// API-equivalent estimate; see the package docs.
type BillingMode string

const (
	// BillingMetered is pay-per-token usage (OpenRouter): its cost is money
	// spent.
	BillingMetered BillingMode = "metered"
	// BillingSubscription is usage on a flat-rate plan (a Claude Max or
	// ChatGPT subscription): its cost is only an API-equivalent estimate,
	// and must never be added to metered cost.
	BillingSubscription BillingMode = "subscription"
)

// Harness is what ran the model. It is a metric label, so the set is fixed.
type Harness string

const (
	HarnessClaude     Harness = "claude"
	HarnessPi         Harness = "pi"
	HarnessSlopReview Harness = "slop-review"
	// HarnessApp is an app's own server calling a model for one of its
	// features (a chat feature, say), rather than an agent's session. It
	// uses no tools the table maps, so its tools are all CategoryOther.
	HarnessApp Harness = "app"
)

var (
	validBillingModes = map[BillingMode]bool{BillingMetered: true, BillingSubscription: true}
	validHarnesses    = map[Harness]bool{HarnessClaude: true, HarnessPi: true, HarnessSlopReview: true, HarnessApp: true}
)

// Record is one unit of usage for one model: the full totals so far for
// one transcript (or run), not an increment. It is the wire format a
// client reports, and the shape a central service would accept unchanged.
//
// Token counts are disjoint except reasoning: InputTokens excludes cache
// reads and writes (Anthropic's convention -- an OpenAI-style client
// subtracts its cached tokens), so input + cache read + cache write is the
// whole prompt. ReasoningTokens is the part of OutputTokens spent thinking
// (both providers bill thinking as output); it is never priced or summed
// separately.
type Record struct {
	SchemaVersion int         `json:"schema_version"`
	Provider      string      `json:"provider"`
	Model         string      `json:"model"`
	Harness       Harness     `json:"harness"`
	BillingMode   BillingMode `json:"billing_mode"`

	Requests         int64 `json:"requests"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`

	// ReportedCostMicros is what the provider says it charged, when it says
	// (nil otherwise).
	ReportedCostMicros *int64 `json:"reported_cost_microdollars,omitempty"`
	// EstimatedCostMicros, PriceVersion and Unpriced are set by the
	// receiver's Pricer, never trusted from the client. An unknown model is
	// Unpriced with no estimate, rather than a guess.
	EstimatedCostMicros *int64 `json:"estimated_cost_microdollars,omitempty"`
	PriceVersion        string `json:"price_version,omitempty"`
	Unpriced            bool   `json:"unpriced,omitempty"`

	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`

	Tools []ToolUsage `json:"tools,omitempty"`
}

// ToolUsage is the totals for one tool (and, for bash, one command) within
// a Record.
type ToolUsage struct {
	// Category is set by the receiver with Categorize; a client leaves it
	// empty and sends the raw Tool name, so the mapping lives in one place.
	Category string `json:"category,omitempty"`
	// Tool is the raw tool name as the harness reported it, e.g. "Bash" or
	// "mcp__slop-haus__ticket_get".
	Tool string `json:"tool"`
	// Command is the program (and allowlisted subcommand) a bash call ran,
	// extracted on the client so full command lines never leave the
	// machine; see ValidCommand. Empty for other tools.
	Command     string `json:"command,omitempty"`
	Calls       int64  `json:"calls"`
	Errors      int64  `json:"errors"`
	DurationMs  int64  `json:"duration_ms"`
	ResultBytes int64  `json:"result_bytes"`
}

// RecordKey is a record's identity within one transcript: a report holds at
// most one record per key, and a Store's rows are unique on it.
type RecordKey struct {
	Provider    string
	Model       string
	Harness     Harness
	BillingMode BillingMode
}

// Key returns the record's identity.
func (r *Record) Key() RecordKey {
	return RecordKey{Provider: r.Provider, Model: r.Model, Harness: r.Harness, BillingMode: r.BillingMode}
}

type toolKey struct {
	tool    string
	command string
}

func (u *ToolUsage) key() toolKey { return toolKey{tool: u.Tool, command: u.Command} }

// Limits. Generous enough for any real transcript, tight enough that a
// buggy or hostile client can't overflow a sum or a column.
const (
	MaxRecordsPerReport = 64
	MaxToolsPerRecord   = 512
	maxCount            = int64(1e12)
	// MaxCostMicros ($10B) is above anything the price table can make of
	// counts within their limits, and low enough that a report's records
	// sum without overflowing int64.
	MaxCostMicros  = int64(1e16)
	maxDurationMs  = int64(1e12)
	maxResultBytes = int64(1e15)
)

var (
	providerPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	modelPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+\[\]-]{0,127}$`)
	toolPattern         = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:-]{0,127}$`)
	priceVersionPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{0,64}$`)
	earliestTime        = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	latestTime          = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
)

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("llmusage: invalid record")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Validate checks a record a client reported: patterns and length caps on
// every string, non-negative counts and sane upper bounds. Receivers run it
// on everything they accept.
func (r *Record) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return invalid("schema_version %d, want %d", r.SchemaVersion, SchemaVersion)
	}
	if err := r.validateLabels(); err != nil {
		return err
	}
	counts := []struct {
		name string
		v    int64
	}{
		{"requests", r.Requests},
		{"input_tokens", r.InputTokens},
		{"output_tokens", r.OutputTokens},
		{"cache_read_tokens", r.CacheReadTokens},
		{"cache_write_tokens", r.CacheWriteTokens},
		{"reasoning_tokens", r.ReasoningTokens},
	}
	for _, c := range counts {
		if err := checkRange(c.name, c.v, maxCount); err != nil {
			return err
		}
	}
	if r.ReasoningTokens > r.OutputTokens {
		return invalid("reasoning_tokens %d exceed output_tokens %d", r.ReasoningTokens, r.OutputTokens)
	}
	for name, p := range map[string]*int64{
		"reported_cost_microdollars":  r.ReportedCostMicros,
		"estimated_cost_microdollars": r.EstimatedCostMicros,
	} {
		if p == nil {
			continue
		}
		if err := checkRange(name, *p, MaxCostMicros); err != nil {
			return err
		}
	}
	if !priceVersionPattern.MatchString(r.PriceVersion) {
		return invalid("price_version %q", r.PriceVersion)
	}
	if r.StartedAt.Before(earliestTime) || r.StartedAt.After(latestTime) {
		return invalid("started_at %s out of range", r.StartedAt)
	}
	if r.EndedAt.Before(r.StartedAt) || r.EndedAt.After(latestTime) {
		return invalid("ended_at %s before started_at or out of range", r.EndedAt)
	}
	if len(r.Tools) > MaxToolsPerRecord {
		return invalid("%d tools, at most %d", len(r.Tools), MaxToolsPerRecord)
	}
	seen := make(map[toolKey]bool, len(r.Tools))
	for i := range r.Tools {
		u := &r.Tools[i]
		if err := u.validate(); err != nil {
			return err
		}
		if seen[u.key()] {
			return invalid("tool %q command %q listed twice", u.Tool, u.Command)
		}
		seen[u.key()] = true
	}
	return nil
}

// validateLabels checks the fields Add turns into label values, so a
// delta that skipped Validate can't add an unbounded or malformed series.
func (r *Record) validateLabels() error {
	switch {
	case !providerPattern.MatchString(r.Provider):
		return invalid("provider %q", r.Provider)
	case !modelPattern.MatchString(r.Model):
		return invalid("model %q", r.Model)
	case !validHarnesses[r.Harness]:
		return invalid("harness %q", r.Harness)
	case !validBillingModes[r.BillingMode]:
		return invalid("billing_mode %q", r.BillingMode)
	}
	for i := range r.Tools {
		if !toolPattern.MatchString(r.Tools[i].Tool) {
			return invalid("tool %q", r.Tools[i].Tool)
		}
	}
	return nil
}

func (u *ToolUsage) validate() error {
	if !toolPattern.MatchString(u.Tool) {
		return invalid("tool %q", u.Tool)
	}
	if u.Category != "" && !ValidCategory(u.Category) {
		return invalid("tool %q category %q", u.Tool, u.Category)
	}
	if u.Command != "" && !ValidCommand(u.Command) {
		return invalid("tool %q command %q", u.Tool, u.Command)
	}
	for _, c := range []struct {
		name string
		v    int64
		max  int64
	}{
		{"calls", u.Calls, maxCount},
		{"errors", u.Errors, maxCount},
		{"duration_ms", u.DurationMs, maxDurationMs},
		{"result_bytes", u.ResultBytes, maxResultBytes},
	} {
		if err := checkRange("tool "+u.Tool+" "+c.name, c.v, c.max); err != nil {
			return err
		}
	}
	if u.Errors > u.Calls {
		return invalid("tool %q errors %d exceed calls %d", u.Tool, u.Errors, u.Calls)
	}
	return nil
}

func checkRange(name string, v, max int64) error {
	if v < 0 || v > max {
		return invalid("%s %d outside [0, %d]", name, v, max)
	}
	return nil
}

// ValidateReport checks a whole report: each record, between one and
// MaxRecordsPerReport of them, and no two with the same Key. An empty
// report is refused because, as full totals, it would wipe what is stored.
func ValidateReport(records []Record) error {
	if len(records) == 0 || len(records) > MaxRecordsPerReport {
		return invalid("%d records, want 1 to %d", len(records), MaxRecordsPerReport)
	}
	seen := make(map[RecordKey]bool, len(records))
	for i := range records {
		if err := records[i].Validate(); err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
		k := records[i].Key()
		if seen[k] {
			return invalid("record %d repeats provider %q model %q harness %q billing_mode %q", i, k.Provider, k.Model, k.Harness, k.BillingMode)
		}
		seen[k] = true
	}
	return nil
}

// Clone returns a deep copy, so a caller's record is never mutated through
// a shared Tools slice or cost pointer.
func (r *Record) Clone() *Record {
	c := *r
	c.ReportedCostMicros = clonePtr(r.ReportedCostMicros)
	c.EstimatedCostMicros = clonePtr(r.EstimatedCostMicros)
	if r.Tools != nil {
		c.Tools = append([]ToolUsage(nil), r.Tools...)
	}
	return &c
}

func clonePtr(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
