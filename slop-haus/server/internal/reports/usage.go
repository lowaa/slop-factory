package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// LLM usage, read from llm_usage and llm_tool_usage.
//
// Raw, bounded reads again, and not a rollup: llm_usage already holds one
// row per transcript x model with the transcript's latest full totals --
// each report replaces the transcript's rows -- so summing the rows in a
// period can never count a report twice, and a re-reported, pruned or
// re-attributed transcript is right by construction. A rollup would have to
// apply the change between two reports (go-llm-usage's Delta) on that same
// hot path and repair itself on every prune; it is worth building only if
// the volume test's budget stops holding.
//
// How a figure is counted:
//
//   - A row belongs to the period its ended_at falls in: its last recorded
//     activity, from the transcript's latest report. A transcript still
//     running counts in the period of its latest report and moves to a later
//     one if it keeps going, so each row is in exactly one period at any
//     time; a period is [from, to), so a row ending exactly on the boundary
//     is the next period's.
//   - Cost is the harness's own figure where it gave one, otherwise the
//     server's estimate. Metered and subscription cost are never summed:
//     every figure is per billing mode, and a subscription's is an
//     API-equivalent estimate, not money spent.
//   - A row with neither figure is unpriced. It adds no cost, and the figure
//     says how many rows (and requests) it leaves out; it is not a zero.
//   - Usage with no ticket (overhead, or a ticket since deleted) is its own
//     line, not dropped.
//   - Guidance (llm_guidance_usage) is usage a console or conductor declared
//     it was spending on a ticket. Its transcript's report is still an
//     overhead llm_usage row, so the totals already hold it; it is shown as
//     its own line and taken out of the unattributed line, which would
//     otherwise count it twice. A guidance row is in the period its own
//     ended_at falls in, the same rule as above. That can differ from its
//     overhead row's period (the overhead row moves on with every later
//     report, the guidance row only with a later charge), so the
//     subtraction never goes below zero.
//   - Per ticket, guidance is a second set of figures beside the worker's,
//     never added to them, and the list is ranked by worker tokens: a ticket
//     with guidance only is listed, after those with any worker usage.

// Billing modes, as go-llm-usage names them.
const (
	BillingMetered      = "metered"
	BillingSubscription = "subscription"
)

// MaxUsageGroups caps the agent and model breakdowns.
const MaxUsageGroups = 100

// MaxUsageTickets caps one page of the per-ticket list.
const MaxUsageTickets = 100

// UsageFilter is the period a usage report covers: [From, To).
type UsageFilter struct {
	From, To time.Time
	// IncludeUnreliable counts the rows flagged as over-counted (the
	// unreliable column); by default they are left out.
	IncludeUnreliable bool
}

// OvercountCutoff is when the fix for the over-counting of conductor and
// supervisor-slot usage reached prod. Usage those agents reported
// before it was flagged unreliable when the column was added, using the same
// instant. Usage reported after it by a reporter that still over-counts is
// flagged when it arrives (llmusage.ReporterUnreliable), and what hosts
// reported after this instant before they were updated was flagged later.
var OvercountCutoff = time.Date(2026, time.October, 22, 0, 48, 7, 0, time.UTC)

// UsageSums is the usage of one set of rows in one billing mode.
type UsageSums struct {
	BillingMode string
	// Records is the rows summed (a transcript has one per model; on the
	// per-day, agent and model figures, the reports that added usage);
	// Transcripts, set only on totals, is the distinct transcripts.
	Records, Transcripts int64

	Requests, InputTokens, OutputTokens                int64
	CacheReadTokens, CacheWriteTokens, ReasoningTokens int64

	// CostMicros is micro-dollars over the priced rows, nil when no row is.
	CostMicros *int64
	// UnpricedRecords and UnpricedRequests are what the cost leaves out.
	UnpricedRecords, UnpricedRequests int64
}

// subtractCharged takes the usage o, already counted in u, back out of it.
// Records and transcripts stay: the rows still exist, they just hold less
// that is nobody's.
func (u *UsageSums) subtractCharged(o *UsageSums) {
	floor := func(v *int64, by int64) { *v = max(*v-by, 0) }
	floor(&u.Requests, o.Requests)
	floor(&u.InputTokens, o.InputTokens)
	floor(&u.OutputTokens, o.OutputTokens)
	floor(&u.CacheReadTokens, o.CacheReadTokens)
	floor(&u.CacheWriteTokens, o.CacheWriteTokens)
	floor(&u.ReasoningTokens, o.ReasoningTokens)
	floor(&u.UnpricedRequests, o.UnpricedRequests)
	if u.CostMicros != nil && o.CostMicros != nil {
		floor(u.CostMicros, *o.CostMicros)
	}
}

func (u *UsageSums) add(o *UsageSums) {
	u.Records += o.Records
	u.Transcripts += o.Transcripts
	u.Requests += o.Requests
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
	u.ReasoningTokens += o.ReasoningTokens
	u.UnpricedRecords += o.UnpricedRecords
	u.UnpricedRequests += o.UnpricedRequests
	if o.CostMicros != nil {
		sum := *o.CostMicros
		if u.CostMicros != nil {
			sum += *u.CostMicros
		}
		u.CostMicros = &sum
	}
}

// UsageDay is one UTC day's usage in one billing mode.
type UsageDay struct {
	Day time.Time
	UsageSums
}

// UsageAgent is one agent's usage in one billing mode. Name is empty for an
// agent that no longer exists: what it cost outlives it.
type UsageAgent struct {
	AgentID string
	Name    string
	UsageSums
}

// UsageModel is one model's usage in one billing mode.
type UsageModel struct {
	Provider, Model string
	UsageSums
}

// UsageTool is the tool use in one category.
type UsageTool struct {
	Category                  string
	Calls, Errors, DurationMs int64
}

// UsageDoneTickets is what finished tickets cost in one billing mode.
type UsageDoneTickets struct {
	BillingMode string
	Tickets     int64
	// CostMicros is the cost of those with a priced row, CostP50Micros the
	// median per ticket; nil when none has.
	CostMicros, CostP50Micros *int64
	// UnpricedTickets is how many have a row without a price, so their cost
	// is understated.
	UnpricedTickets int64
}

// UsageReport is the LLM usage section but for the per-ticket list.
type UsageReport struct {
	From, To time.Time
	// Totals is every row, per billing mode; Unattributed the part of it
	// with no ticket.
	Totals       []UsageSums
	Unattributed []UsageSums
	// Guidance is the usage charged to tickets by a declaration, per billing
	// mode, and is part of Totals; Unattributed has it taken out.
	Guidance    []UsageSums
	Days        []UsageDay
	Agents      []UsageAgent
	Models      []UsageModel
	Tools       []UsageTool
	DoneTickets []UsageDoneTickets
	// Tickets is how many tickets have worker or guidance usage in the period.
	Tickets int64
	// Unreliable is how many usage and guidance rows in the period are flagged
	// over-counted, whether or not they were counted.
	Unreliable int64
}

// UsageTicket is one ticket's usage, per billing mode.
type UsageTicket struct {
	TicketID int64
	Title    string
	Status   string
	// Modes is the worker's usage; Guidance what consoles and conductors
	// declared they spent on the ticket. Never summed together.
	Modes    []UsageSums
	Guidance []UsageSums
}

// UsageTicketPage is a page of tickets, most tokens first.
type UsageTicketPage struct {
	Items []UsageTicket
	// Total is how many tickets match the filter and have usage in the
	// period, not just this page.
	Total int64
	// Next is where the following page starts; nil on the last.
	Next *UsageTicketCursor
}

// UsageTicketQuery is which tickets, and which page of them.
type UsageTicketQuery struct {
	// Filter is the ticket filter every ticket list takes; its sort and page
	// fields are ignored (usage lists are ranked by tokens).
	Filter *ticket.ListFilter
	Limit  int
	After  *UsageTicketCursor
	// IncludeUnreliable is UsageFilter's, for the ticket list.
	IncludeUnreliable bool
}

// LLMUsage reports the last days days of LLM usage, ending now.
func (s *Service) LLMUsage(ctx context.Context, days int, includeUnreliable bool) (*UsageReport, error) {
	f, err := s.usagePeriod(days)
	if err != nil {
		return nil, err
	}
	f.IncludeUnreliable = includeUnreliable
	return s.store.LLMUsage(ctx, f)
}

// LLMUsageTickets pages the tickets that match q.Filter and have usage in the
// last days days.
func (s *Service) LLMUsageTickets(ctx context.Context, days int, q *UsageTicketQuery) (*UsageTicketPage, error) {
	f, err := s.usagePeriod(days)
	if err != nil {
		return nil, err
	}
	if q.Limit < 1 || q.Limit > MaxUsageTickets {
		return nil, fmt.Errorf("%w: limit %d", ErrInvalid, q.Limit)
	}
	if err := q.Filter.Validate(); err != nil {
		return nil, err
	}
	f.IncludeUnreliable = q.IncludeUnreliable
	query := usageQueryHash(days, q)
	if q.After != nil && q.After.Query != query {
		return nil, fmt.Errorf("%w: cursor is for a different query", ErrInvalid)
	}
	page, err := s.store.LLMUsageTickets(ctx, f, q)
	if err != nil {
		return nil, err
	}
	if page.Next != nil {
		page.Next.Query = query
	}
	return page, nil
}

func (s *Service) usagePeriod(days int) (*UsageFilter, error) {
	valid := false
	for _, p := range Periods {
		valid = valid || p == days
	}
	if !valid {
		return nil, fmt.Errorf("%w: days %d", ErrInvalid, days)
	}
	to := s.now().UTC()
	return &UsageFilter{From: to.AddDate(0, 0, -days), To: to}, nil
}
