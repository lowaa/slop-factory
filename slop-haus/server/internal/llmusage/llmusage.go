// Package llmusage stores the LLM usage agents report for their
// transcripts, attributes each transcript to the ticket the agent was
// working, and counts it in the shared llm_* metrics.
//
// The record format, validation, pricing, the delta between two reports
// and the metrics come from go-llm-usage, whose Service runs the ingest;
// this package implements its Store over slop-haus's tables and adds what
// is slop-haus's own: the agent, the session, and the ticket.
//
// Who a report is for -- tenant and agent -- comes from the token it was
// sent with, never from its body.
package llmusage

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	usagelib "github.com/lowaa/slop-factory/go-llm-usage"
)

// Limits on one report, tighter than go-llm-usage's own: a harness reports
// a handful of models per transcript and a few dozen distinct tools.
const (
	MaxRecordsPerReport = 32
	MaxToolsPerReport   = 1024
	// MaxBodyBytes caps the route's request body. A report at both limits
	// above is well under it.
	MaxBodyBytes = 1 << 20
)

// Scope is what a transcript was attributed to.
type Scope string

const (
	// ScopeTicket is a transcript that overlapped exactly one of the
	// agent's tickets.
	ScopeTicket Scope = "ticket"
	// ScopeOverhead is everything else: a transcript that overlapped none
	// of the agent's tickets (checking in, waiting for work), or several
	// (a session that worked several tickets in turn).
	ScopeOverhead Scope = "overhead"
)

// Transcript says whose transcript a report is for. AgentID comes from the
// caller's token; the rest is what the client said.
type Transcript struct {
	AgentID uuid.UUID
	Key     usagelib.TranscriptKey
	Harness usagelib.Harness
	// SessionID is the harness's session id; Key adds a subagent's id to it
	// where there is one.
	SessionID string
	// ScopeHint is "main" or "subagent:<type>". Stored as the client said;
	// attribution goes by the agent's leases, not by this.
	ScopeHint string
	// ConversationID is set when a conductor's host reports the usage of one
	// of its conversations. The attribution then follows the conversation,
	// not leases (a conductor holds none): it is overhead, since a Remote
	// Control session is about no ticket.
	ConversationID *uuid.UUID
	// Unreliable is set from what the report says sent it (see
	// ReporterUnreliable), not by the client: the usage it adds is flagged
	// as over-counted.
	Unreliable bool
}

// Attribution is the ticket a transcript was charged to.
type Attribution struct {
	Scope Scope
	// TicketID is set when Scope is ScopeTicket.
	TicketID *int64
	// Attempt is the attempt of that ticket the lease was for.
	Attempt *int
}

// Report is one usage report: full totals, per model, for one transcript.
type Report struct {
	Transcript Transcript
	Records    []usagelib.Record
}

// Sources a report names itself as sent by. A hook is a Stop hook, as
// console and pi sessions send.
const (
	SourceSupervisor = "supervisor"
	SourceHost       = "host"
	SourceHook       = "hook"
)

// MinReporterRevision is the first revision of the supervisor and host
// reporters that counts correctly (the over-count fix and the baseline reset).
// Bump it when a later fix means older reporters over-count again.
const MinReporterRevision = 1

// ReporterUnreliable says whether a report was sent by a supervisor or host
// that over-counts, by its own word: the server cannot see what code a host
// runs. A report that names no source is from a client that predates the
// field and is trusted, as the deploy time of its fix is all there is to go
// by (what was stored by then was flagged unreliable afterwards).
func ReporterUnreliable(source string, revision int) bool {
	return (source == SourceSupervisor || source == SourceHost) && revision < MinReporterRevision
}

var (
	ErrInvalid     = errors.New("llmusage: invalid report")
	ErrRateLimited = errors.New("llmusage: too many reports")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

var (
	sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:@+-]{1,128}$`)
	scopeHintPattern = regexp.MustCompile(`^(main|subagent:[A-Za-z0-9._-]{1,64})$`)
)

// validate checks what go-llm-usage's Service does not: the transcript's
// own fields, that every record is from the harness the report names, and
// this package's tighter limits. The Service validates the key and each
// record after this.
func (r *Report) validate() error {
	t := &r.Transcript
	if err := t.Key.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if !sessionIDPattern.MatchString(t.SessionID) {
		return invalid("session_id %q", t.SessionID)
	}
	if !scopeHintPattern.MatchString(t.ScopeHint) {
		return invalid("scope %q, want main or subagent:<type>", t.ScopeHint)
	}
	if c := t.ConversationID; c != nil {
		// The harness's session id is the conversation's, and a transcript of
		// it (or of a subagent in it) is keyed by it.
		id := c.String()
		if t.SessionID != id {
			return invalid("session_id %q is not the conversation's", t.SessionID)
		}
		if k := string(t.Key); k != id && !strings.HasPrefix(k, id+":") {
			return invalid("transcript_key %q is not the conversation's", k)
		}
	}
	if len(r.Records) == 0 || len(r.Records) > MaxRecordsPerReport {
		return invalid("%d records, want 1 to %d", len(r.Records), MaxRecordsPerReport)
	}
	tools := 0
	for i := range r.Records {
		if r.Records[i].Harness != t.Harness {
			return invalid("record %d is from harness %q, the report from %q", i, r.Records[i].Harness, t.Harness)
		}
		tools += len(r.Records[i].Tools)
	}
	if tools > MaxToolsPerReport {
		return invalid("%d tools, at most %d", tools, MaxToolsPerReport)
	}
	return nil
}

// window is the span the report's records cover: the earliest start to the
// latest end.
func window(records []usagelib.Record) (start, end time.Time) {
	for i := range records {
		if i == 0 || records[i].StartedAt.Before(start) {
			start = records[i].StartedAt
		}
		if i == 0 || records[i].EndedAt.After(end) {
			end = records[i].EndedAt
		}
	}
	return start, end
}
