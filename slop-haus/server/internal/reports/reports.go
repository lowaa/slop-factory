// Package reports answers the Reports tab: lead time, time in each state and
// throughput, read from ticket_status_changes.
//
// The queries are user-facing -- they run on every page view over a table
// that only grows -- so they are raw reads bounded by the selected period
// rather than a maintained rollup. Each starts from an index range on the
// period's changes and does per-row lookups on (tenant_id, ticket_id, id), so
// cost follows the period, not the tenant's history. A rollup would need a
// quantile sketch per (day, state, priority), a second write on the trigger
// path of every status change, and a correction when a ticket is reopened;
// it is worth building only if the volume test's budget stops holding.
//
// How a figure is counted:
//
//   - A stay in a state belongs to the period it ENDS in, so a ticket that
//     entered review last month and left it this week counts this week.
//   - A stay still going counts up to the period's end (now), and the
//     figures say how many such stays they include.
//   - Done and cancelled are not timed: nothing waits in them.
//   - Lead time is the ticket's creation to a change into done; a ticket
//     reopened and finished again counts again.
//   - Parked is left out unless asked for: no time in it, no count among the
//     tickets now, and a done ticket's lead time (and first-claimed-to-done)
//     has the time it spent parked taken off. The ticket still counts.
//   - Figures with nothing behind them are absent (nil), never zero.
package reports

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Periods are the lengths, in days, a report can cover.
var Periods = []int{7, 30, 90}

// Priorities are the values a report can be filtered on.
var Priorities = []string{"low", "normal", "high", "urgent"}

// MaxWaiting caps one page of the longest-waiting list.
const MaxWaiting = 50

// MaxWaitingOffset caps how deep into the list a page can start.
const MaxWaitingOffset = 1000

var (
	ErrInvalid = errors.New("invalid report parameters")
	// ErrTooSlow means a report query hit its statement timeout.
	ErrTooSlow = errors.New("report took too long")
)

// Filter is what a report is computed for. Priority is empty for all.
type Filter struct {
	From, To time.Time
	Priority string
	// IncludeParked counts parked tickets like any other. Parked is a holding
	// area where tickets sit on purpose, so by default their time in it is
	// left out of every figure and their lead times are measured without it.
	IncludeParked bool
}

// Percentiles are the p50 and p90 in seconds.
type Percentiles struct {
	P50, P90 float64
}

// StateFigure is the time spent in one state.
type StateFigure struct {
	State string
	// Stays is how many stays the figures are over, Ongoing how many of them
	// are still going and so counted up to the period's end.
	Stays, Ongoing int64
	// Duration is nil when there are no stays.
	Duration *Percentiles
	// TotalSeconds is the sum of every stay's time; a state's share of the
	// whole is its total over the sum of all states'.
	TotalSeconds float64
}

// LeadFigure is one set of lead times: overall, or one week's.
type LeadFigure struct {
	// WeekStart is set for a week's figure and zero for the overall one.
	WeekStart time.Time
	// Done is the changes into done in the set.
	Done int64
	// CreatedToDone is nil when Done is zero.
	CreatedToDone *Percentiles
	// ClaimedToDone is over the done tickets with a recorded first claim,
	// ClaimedDone of them; nil when none.
	ClaimedToDone *Percentiles
	ClaimedDone   int64
}

// DayCount is tickets done on one UTC day.
type DayCount struct {
	Day  time.Time
	Done int64
}

// WhereNow is the tickets in one not-yet-finished status now.
type WhereNow struct {
	Status string
	Count  int64
	// WithoutHistory is how many of them have no recorded change, because
	// they were last moved before history was kept: they cannot be told how
	// long they have waited.
	WithoutHistory int64
}

// Summary is everything on the Reports tab but the waiting lists.
type Summary struct {
	From, To time.Time
	// HistoryFrom is the first change ever recorded for the tenant; nil if
	// none. Nothing before it is known, and the page says so.
	HistoryFrom *time.Time
	Lead        LeadFigure
	LeadByWeek  []LeadFigure
	States      []StateFigure
	Throughput  []DayCount
	Now         []WhereNow
	// IncludeParked is whether the figures count parked tickets and their
	// time (see Filter).
	IncludeParked bool
	// ParkedNotShown is how many tickets are parked now, left out of Now when
	// IncludeParked is false so the count is not silently lost.
	ParkedNotShown int64
}

// Waiting is one ticket in a status and since when.
type Waiting struct {
	TicketID int64
	Title    string
	Priority string
	Since    time.Time
}

// WaitingPage is a page of the tickets that have waited longest in a status.
type WaitingPage struct {
	Items []Waiting
	// Total is how many in the status have a recorded time, not just this
	// page.
	Total int64
}

// Store is the persistence Service needs. Every call runs in the context's
// tenant, and row level security hides every other tenant's rows.
type Store interface {
	Summary(ctx context.Context, f *Filter) (*Summary, error)
	Waiting(ctx context.Context, status string, priority string, limit, offset int) (*WaitingPage, error)
	LLMUsage(ctx context.Context, f *UsageFilter) (*UsageReport, error)
	LLMUsageTickets(ctx context.Context, f *UsageFilter, q *UsageTicketQuery) (*UsageTicketPage, error)
	ScopeCostRows(ctx context.Context, from, to time.Time) (*ScopeCostInput, error)
	LockHistoryRows(ctx context.Context, from, to time.Time) (*LockHistoryInput, error)
	ShadowDecisions(ctx context.Context, from, to time.Time) (*ShadowDecisions, error)
}

// Service validates a request and works out its period.
type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

func validPriority(p string) bool {
	if p == "" {
		return true
	}
	for _, v := range Priorities {
		if v == p {
			return true
		}
	}
	return false
}

// Summary reports the last days days, ending now.
func (s *Service) Summary(ctx context.Context, days int, priority string, includeParked bool) (*Summary, error) {
	valid := false
	for _, p := range Periods {
		valid = valid || p == days
	}
	if !valid || !validPriority(priority) {
		return nil, fmt.Errorf("%w: days %d, priority %q", ErrInvalid, days, priority)
	}
	to := s.now().UTC()
	return s.store.Summary(ctx, &Filter{From: to.AddDate(0, 0, -days), To: to, Priority: priority, IncludeParked: includeParked})
}

// waitingStatuses are the statuses tickets can wait in.
var waitingStatuses = []string{"preparing", "parked", "open", "in_progress", "in_merge_queue", "blocked", "discussion", "review"}

// Waiting pages the tickets that have been in status longest.
func (s *Service) Waiting(ctx context.Context, status, priority string, includeParked bool, limit, offset int) (*WaitingPage, error) {
	known := status != "parked" || includeParked
	if known {
		known = false
		for _, v := range waitingStatuses {
			known = known || v == status
		}
	}
	if !known || !validPriority(priority) || limit < 1 || limit > MaxWaiting || offset < 0 || offset > MaxWaitingOffset {
		return nil, fmt.Errorf("%w: status %q, priority %q, limit %d, offset %d", ErrInvalid, status, priority, limit, offset)
	}
	return s.store.Waiting(ctx, status, priority, limit, offset)
}
