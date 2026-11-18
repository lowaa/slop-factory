package llmusage

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"github.com/danielgtaylor/huma/v2"

	usagelib "github.com/lowaa/slop-factory/go-llm-usage"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wireschema"
)

// Handler is a thin HTTP wrapper over Service.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

// UsageBody is one usage report. The agent it is for is the token's; no
// field here can name another.
type UsageBody struct {
	// TranscriptKey is unique per agent: the harness session id, plus a
	// subagent's id where there is one. Reports for one key overwrite each
	// other.
	TranscriptKey string        `json:"transcript_key" maxLength:"256"`
	Harness       ReportHarness `json:"harness"`
	SessionID     string        `json:"session_id" maxLength:"128"`
	// Scope is main, or subagent:<type> for a subagent's transcript.
	Scope string `json:"scope" maxLength:"80"`
	// ConversationID is the conductor conversation the transcript is of,
	// reported by that conductor's host: the usage is then overhead. The session id must be the conversation's.
	ConversationID string `json:"conversation_id,omitempty" format:"uuid"`
	// Source is what sent the report. A supervisor or host sends its
	// ReporterRevision too, and one older than the fix for an over-count is
	// flagged, whatever the time. Left out by older clients.
	Source           string `json:"source,omitempty" enum:"supervisor,host,hook"`
	ReporterRevision int    `json:"reporter_revision,omitempty" minimum:"0" maximum:"1000000"`
	// Records are the transcript's full totals so far, one per model.
	Records []usagelib.Record `json:"records" maxItems:"32"`
}

type IngestUsageRequest struct {
	Body UsageBody
}

type IngestUsageResponse struct {
	Body struct {
		// Scope is what the transcript was attributed to: ticket or
		// overhead.
		Scope string `json:"scope" enum:"ticket,overhead"`
		// TicketID is the ticket, when Scope is ticket.
		TicketID *int64 `json:"ticket_id,omitempty"`
	}
}

func (h *Handler) IngestUsage(ctx context.Context, input *IngestUsageRequest) (*IngestUsageResponse, error) {
	b := &input.Body
	b.Harness = canonicalHarness(b.Harness)
	for i := range b.Records {
		b.Records[i].Harness = usagelib.Harness(canonicalHarness(ReportHarness(b.Records[i].Harness)))
	}
	transcript := Transcript{
		Key:        usagelib.TranscriptKey(b.TranscriptKey),
		Harness:    usagelib.Harness(b.Harness),
		SessionID:  b.SessionID,
		ScopeHint:  b.Scope,
		Unreliable: ReporterUnreliable(b.Source, b.ReporterRevision),
	}
	if b.ConversationID != "" {
		id, err := uuid.Parse(b.ConversationID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("conversation_id must be a valid uuid")
		}
		transcript.ConversationID = &id
	}
	attribution, err := h.svc.Ingest(ctx, &Report{Transcript: transcript, Records: b.Records})
	switch {
	case errors.Is(err, ErrInvalid):
		return nil, huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, ErrRateLimited):
		return nil, huma.Error429TooManyRequests(err.Error())
	case err != nil:
		h.logger.Error("ingest llm usage", "err", err)
		return nil, huma.Error500InternalServerError("internal server error")
	}
	resp := &IngestUsageResponse{}
	resp.Body.Scope = string(attribution.Scope)
	resp.Body.TicketID = attribution.TicketID
	return resp, nil
}

// ReportHarness is what a usage report names as its source: a registered
// harness, or slop-review, which reports usage but is not a harness an
// agent runs in.
type ReportHarness string

const (
	harnessSlopReview ReportHarness = "slop-review"
	// harnessLegacySelfReview is what reporters from before the rename send.
	// The schema still accepts it so those reporters are not refused; ingest
	// stores it as slop-review.
	harnessLegacySelfReview ReportHarness = "self-review"
)

// canonicalHarness maps a legacy harness name to its current one. It runs
// before validation, which only knows current names and compares each
// record's harness to the report's.
func canonicalHarness(h ReportHarness) ReportHarness {
	if h == harnessLegacySelfReview {
		return harnessSlopReview
	}
	return h
}

func (ReportHarness) Schema(r huma.Registry) *huma.Schema {
	var values []ReportHarness
	for _, n := range harness.Names() {
		values = append(values, ReportHarness(n))
	}
	return wireschema.Enum(r, "UsageHarness", append(values, harnessSlopReview, harnessLegacySelfReview))
}
