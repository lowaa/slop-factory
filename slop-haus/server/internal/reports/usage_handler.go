package reports

import (
	"context"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// UsageSumsBody is the usage of one set of rows in one billing mode.
type UsageSumsBody struct {
	// BillingMode is "metered" (money spent) or "subscription" (an
	// API-equivalent estimate, not money spent). Figures of the two are
	// never added together.
	BillingMode string `json:"billing_mode"`
	// Records is the transcript x model rows summed; Transcripts is set only
	// on the totals.
	Records     int64 `json:"records"`
	Transcripts int64 `json:"transcripts,omitempty"`

	Requests         int64 `json:"requests"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	// ReasoningTokens is part of OutputTokens, not on top of it.
	ReasoningTokens int64 `json:"reasoning_tokens"`

	// CostMicros is micro-dollars over the priced records; absent when none
	// is priced, which is not zero.
	CostMicros *int64 `json:"cost_micros"`
	// UnpricedRecords and UnpricedRequests are the usage the cost leaves out.
	UnpricedRecords  int64 `json:"unpriced_records"`
	UnpricedRequests int64 `json:"unpriced_requests"`
}

func usageSumsBody(u *UsageSums) UsageSumsBody {
	return UsageSumsBody{
		BillingMode: u.BillingMode, Records: u.Records, Transcripts: u.Transcripts, Requests: u.Requests,
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens, ReasoningTokens: u.ReasoningTokens, CostMicros: u.CostMicros,
		UnpricedRecords: u.UnpricedRecords, UnpricedRequests: u.UnpricedRequests,
	}
}

// UsageSumsBodies is the wire form of a set of per-billing-mode figures.
func UsageSumsBodies(in []UsageSums) []UsageSumsBody { return usageSumsBodies(in) }

func usageSumsBodies(in []UsageSums) []UsageSumsBody {
	out := make([]UsageSumsBody, 0, len(in))
	for i := range in {
		out = append(out, usageSumsBody(&in[i]))
	}
	return out
}

type UsageDayBody struct {
	Day time.Time `json:"day"`
	UsageSumsBody
}

type UsageAgentBody struct {
	AgentID string `json:"agent_id"`
	// Name is empty for an agent that no longer exists.
	Name string `json:"name"`
	UsageSumsBody
}

type UsageModelBody struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	UsageSumsBody
}

type UsageToolBody struct {
	Category   string `json:"category"`
	Calls      int64  `json:"calls"`
	Errors     int64  `json:"errors"`
	DurationMs int64  `json:"duration_ms"`
}

type UsageDoneTicketsBody struct {
	BillingMode string `json:"billing_mode"`
	Tickets     int64  `json:"tickets"`
	// CostMicros is absent when no done ticket has a priced record.
	CostMicros    *int64 `json:"cost_micros"`
	CostP50Micros *int64 `json:"cost_p50_micros"`
	// UnpricedTickets is how many done tickets have an unpriced record, so
	// their cost is understated.
	UnpricedTickets int64 `json:"unpriced_tickets"`
}

// UsageReportBody is the LLM usage section, every figure over the rows whose
// last activity (ended_at) falls in [from, to).
type UsageReportBody struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Totals has a figure per billing mode with usage in the period.
	Totals []UsageSumsBody `json:"totals"`
	// Unattributed is the part of Totals not charged to a ticket: overhead,
	// or a ticket that has been deleted.
	Unattributed []UsageSumsBody `json:"unattributed"`
	// Guidance is the usage consoles and Remote Control sessions declared
	// they spent on tickets (working_on), per billing mode. It is part of
	// Totals and is not in Unattributed.
	Guidance    []UsageSumsBody        `json:"guidance"`
	Days        []UsageDayBody         `json:"days"`
	Agents      []UsageAgentBody       `json:"agents"`
	Models      []UsageModelBody       `json:"models"`
	Tools       []UsageToolBody        `json:"tools"`
	DoneTickets []UsageDoneTicketsBody `json:"done_tickets"`
	// Tickets is how many tickets have usage; the list is paged separately.
	Tickets int64 `json:"tickets"`
	// UnreliableRecords is how many records in the period are flagged as
	// over-counted, counted or not. UnreliableBefore is when the over-count
	// was fixed: flagged usage is from before it.
	UnreliableRecords int64     `json:"unreliable_records"`
	UnreliableBefore  time.Time `json:"unreliable_before"`
}

func usageReportBody(r *UsageReport) *UsageReportBody {
	b := &UsageReportBody{
		From: r.From.UTC(), To: r.To.UTC(), Tickets: r.Tickets,
		UnreliableRecords: r.Unreliable, UnreliableBefore: OvercountCutoff,
		Totals:       usageSumsBodies(r.Totals),
		Unattributed: usageSumsBodies(r.Unattributed),
		Guidance:     usageSumsBodies(r.Guidance),
		Days:         make([]UsageDayBody, 0, len(r.Days)),
		Agents:       make([]UsageAgentBody, 0, len(r.Agents)),
		Models:       make([]UsageModelBody, 0, len(r.Models)),
		Tools:        make([]UsageToolBody, 0, len(r.Tools)),
		DoneTickets:  make([]UsageDoneTicketsBody, 0, len(r.DoneTickets)),
	}
	for i := range r.Days {
		b.Days = append(b.Days, UsageDayBody{Day: r.Days[i].Day.UTC(), UsageSumsBody: usageSumsBody(&r.Days[i].UsageSums)})
	}
	for i := range r.Agents {
		a := &r.Agents[i]
		b.Agents = append(b.Agents, UsageAgentBody{AgentID: a.AgentID, Name: a.Name, UsageSumsBody: usageSumsBody(&a.UsageSums)})
	}
	for i := range r.Models {
		m := &r.Models[i]
		b.Models = append(b.Models, UsageModelBody{Provider: m.Provider, Model: m.Model, UsageSumsBody: usageSumsBody(&m.UsageSums)})
	}
	for _, t := range r.Tools {
		b.Tools = append(b.Tools, UsageToolBody{Category: t.Category, Calls: t.Calls, Errors: t.Errors, DurationMs: t.DurationMs})
	}
	for _, d := range r.DoneTickets {
		b.DoneTickets = append(b.DoneTickets, UsageDoneTicketsBody{BillingMode: d.BillingMode, Tickets: d.Tickets,
			CostMicros: d.CostMicros, CostP50Micros: d.CostP50Micros, UnpricedTickets: d.UnpricedTickets})
	}
	return b
}

type GetLLMUsageRequest struct {
	// Days is the length of the period, ending now.
	Days int `query:"days" default:"30" enum:"7,30,90"`
	// IncludeUnreliable counts the usage flagged as over-counted, which is
	// left out by default.
	IncludeUnreliable bool `query:"include_unreliable"`
}

type GetLLMUsageResponse struct {
	Body *UsageReportBody
}

func (h *Handler) GetLLMUsage(ctx context.Context, in *GetLLMUsageRequest) (*GetLLMUsageResponse, error) {
	r, err := h.svc.LLMUsage(ctx, in.Days, in.IncludeUnreliable)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &GetLLMUsageResponse{Body: usageReportBody(r)}, nil
}

type UsageTicketBody struct {
	ID     int64         `json:"id"`
	Title  string        `json:"title"`
	Status ticket.Status `json:"status"`
	// Modes is the worker's usage, a figure per billing mode.
	Modes []UsageSumsBody `json:"modes"`
	// GuidanceModes is the usage consoles and Remote Control sessions
	// declared they spent on the ticket, per billing mode, kept apart from
	// the worker's.
	GuidanceModes []UsageSumsBody `json:"guidance_modes"`
}

type UsageTicketPageBody struct {
	Tickets []UsageTicketBody `json:"tickets"`
	// Total is how many tickets match the filter and have usage in the
	// period, of which Tickets is one page.
	Total int64 `json:"total"`
	// NextCursor is set when more follows.
	NextCursor string `json:"next_cursor,omitempty"`
}

// GetLLMUsageTicketsRequest takes the ticket filter every ticket list takes,
// so the by-ticket usage list is filtered by the same widget as the others.
type GetLLMUsageTicketsRequest struct {
	Days int `query:"days" default:"30" enum:"7,30,90"`
	// IncludeUnreliable is GetLLMUsageRequest's.
	IncludeUnreliable bool `query:"include_unreliable"`
	ticket.FilterQuery
	Limit int `query:"limit" default:"50" minimum:"1" maximum:"100"`
	// Cursor is a previous response's next_cursor, valid only for the same
	// period and filter.
	Cursor string `query:"cursor"`
}

type GetLLMUsageTicketsResponse struct {
	Body *UsageTicketPageBody
}

func (h *Handler) GetLLMUsageTickets(ctx context.Context, in *GetLLMUsageTicketsRequest) (*GetLLMUsageTicketsResponse, error) {
	filter, err := in.FilterQuery.Filter()
	if err != nil {
		return nil, h.mapError(err)
	}
	query := &UsageTicketQuery{Filter: filter, Limit: in.Limit, IncludeUnreliable: in.IncludeUnreliable}
	if in.Cursor != "" {
		if query.After, err = DecodeUsageTicketCursor(in.Cursor); err != nil {
			return nil, h.mapError(err)
		}
	}
	page, err := h.svc.LLMUsageTickets(ctx, in.Days, query)
	if err != nil {
		return nil, h.mapError(err)
	}
	body := &UsageTicketPageBody{Total: page.Total, Tickets: make([]UsageTicketBody, 0, len(page.Items))}
	if page.Next != nil {
		body.NextCursor = page.Next.Encode()
	}
	for i := range page.Items {
		t := &page.Items[i]
		body.Tickets = append(body.Tickets, UsageTicketBody{ID: t.TicketID, Title: t.Title, Status: ticket.Status(t.Status),
			Modes: usageSumsBodies(t.Modes), GuidanceModes: usageSumsBodies(t.Guidance)})
	}
	return &GetLLMUsageTicketsResponse{Body: body}, nil
}
