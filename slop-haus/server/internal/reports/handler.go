package reports

import (
	"context"
	"log/slog"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Handler is a thin HTTP wrapper over Service.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrInvalid, Kind: httperr.Invalid},
		{Err: ticket.ErrInvalidEnum, Kind: httperr.Invalid},
		{Err: ticket.ErrInvalidSearch, Kind: httperr.Invalid},
		{Err: ticket.ErrInvalidPage, Kind: httperr.Invalid},
		{Err: ErrTooSlow, Kind: httperr.Unavailable, Message: "the report took too long to compute; try a shorter period"},
	},
	Unexpected: "report",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

// -- Wire types --

// DurationPercentilesBody is a p50 and p90, in seconds.
type DurationPercentilesBody struct {
	P50 float64 `json:"p50_seconds"`
	P90 float64 `json:"p90_seconds"`
}

func percentilesBody(p *Percentiles) *DurationPercentilesBody {
	if p == nil {
		return nil
	}
	return &DurationPercentilesBody{P50: p.P50, P90: p.P90}
}

// LeadTimeBody is a set of lead times: overall, or one week's.
type LeadTimeBody struct {
	// WeekStart is the Monday (UTC) a week's figures start on; absent on the
	// overall figure.
	WeekStart *time.Time `json:"week_start,omitempty"`
	// Done is the changes into done the figures cover.
	Done int64 `json:"done"`
	// CreatedToDone is absent when nothing was done.
	CreatedToDone *DurationPercentilesBody `json:"created_to_done"`
	// ClaimedDone is how many done tickets have a recorded first claim, which
	// ClaimedToDone is over; absent when none do.
	ClaimedDone   int64                    `json:"claimed_done"`
	ClaimedToDone *DurationPercentilesBody `json:"claimed_to_done"`
}

func leadBody(l *LeadFigure) LeadTimeBody {
	b := LeadTimeBody{
		Done:          l.Done,
		CreatedToDone: percentilesBody(l.CreatedToDone),
		ClaimedDone:   l.ClaimedDone,
		ClaimedToDone: percentilesBody(l.ClaimedToDone),
	}
	if !l.WeekStart.IsZero() {
		w := l.WeekStart.UTC()
		b.WeekStart = &w
	}
	return b
}

// StateTimeBody is the time spent in one state.
type StateTimeBody struct {
	State string `json:"state"`
	// Stays is how many stays the figures are over, Ongoing how many of them
	// were still going at the period's end and so are counted up to it.
	Stays   int64 `json:"stays"`
	Ongoing int64 `json:"ongoing"`
	// Duration is absent when there are no stays.
	Duration *DurationPercentilesBody `json:"duration"`
	// Share is this state's part of all the time tickets spent in a state in
	// the period, 0 to 1; absent when none was spent.
	Share *float64 `json:"share"`
}

// DoneDayBody is the tickets done on one UTC day.
type DoneDayBody struct {
	Day  time.Time `json:"day"`
	Done int64     `json:"done"`
}

// WhereNowBody is the tickets in one status now.
type WhereNowBody struct {
	Status ticket.Status `json:"status"`
	Count  int64         `json:"count"`
	// WithoutHistory is how many have no recorded time in the status,
	// because they were last moved before history was kept.
	WithoutHistory int64 `json:"without_history"`
}

type ReportBody struct {
	From time.Time `json:"from"`
	// To is the period's end: stays still going count up to it.
	To time.Time `json:"to"`
	// HistoryFrom is when the first status change was recorded for the
	// tenant, absent if none has been. Nothing before it is known.
	HistoryFrom *time.Time     `json:"history_from"`
	Lead        LeadTimeBody   `json:"lead"`
	LeadByWeek  []LeadTimeBody `json:"lead_by_week"`
	// States holds every state tickets wait in, in board order, with no
	// duration where there were no stays.
	States     []StateTimeBody `json:"states"`
	Throughput []DoneDayBody   `json:"throughput"`
	Now        []WhereNowBody  `json:"now"`
	// ParkedNotShown is how many tickets are parked now and left out of Now
	// because parked was not asked for; zero when it was.
	ParkedNotShown int64 `json:"parked_not_shown"`
}

// timedStates are the states tickets spend time in, in the order the page
// shows them.
var timedStates = []string{"preparing", "parked", "open", "in_progress", "in_merge_queue", "blocked", "discussion", "review"}

func summaryBody(s *Summary) *ReportBody {
	b := &ReportBody{
		From: s.From.UTC(), To: s.To.UTC(),
		Lead:       leadBody(&s.Lead),
		LeadByWeek: make([]LeadTimeBody, 0, len(s.LeadByWeek)),
		States:     make([]StateTimeBody, 0, len(timedStates)),
		Throughput: make([]DoneDayBody, 0, len(s.Throughput)),
		Now:        make([]WhereNowBody, 0, len(s.Now)),

		ParkedNotShown: s.ParkedNotShown,
	}
	if s.HistoryFrom != nil {
		h := s.HistoryFrom.UTC()
		b.HistoryFrom = &h
	}
	for i := range s.LeadByWeek {
		b.LeadByWeek = append(b.LeadByWeek, leadBody(&s.LeadByWeek[i]))
	}
	var total float64
	byState := map[string]*StateFigure{}
	for i := range s.States {
		byState[s.States[i].State] = &s.States[i]
		total += s.States[i].TotalSeconds
	}
	for _, name := range timedStates {
		if name == "parked" && !s.IncludeParked {
			continue
		}
		state := StateTimeBody{State: name}
		if f := byState[name]; f != nil {
			state.Stays, state.Ongoing, state.Duration = f.Stays, f.Ongoing, percentilesBody(f.Duration)
			if total > 0 {
				share := f.TotalSeconds / total
				state.Share = &share
			}
		}
		b.States = append(b.States, state)
	}
	for _, d := range s.Throughput {
		b.Throughput = append(b.Throughput, DoneDayBody{Day: d.Day.UTC(), Done: d.Done})
	}
	for _, n := range s.Now {
		b.Now = append(b.Now, WhereNowBody{Status: ticket.Status(n.Status), Count: n.Count, WithoutHistory: n.WithoutHistory})
	}
	return b
}

type GetReportRequest struct {
	// Days is the length of the period, ending now.
	Days int `query:"days" default:"30" enum:"7,30,90"`
	// Priority limits the report to tickets of one priority.
	Priority string `query:"priority" enum:"low,normal,high,urgent"`
	// IncludeParked counts parked tickets and their time: parked is a
	// holding area, so by default it is left out.
	IncludeParked bool `query:"include_parked" default:"false"`
}

type GetReportResponse struct {
	Body *ReportBody
}

func (h *Handler) GetReport(ctx context.Context, in *GetReportRequest) (*GetReportResponse, error) {
	s, err := h.svc.Summary(ctx, in.Days, in.Priority, in.IncludeParked)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &GetReportResponse{Body: summaryBody(s)}, nil
}

type WaitingTicketBody struct {
	ID       int64     `json:"id"`
	Title    string    `json:"title"`
	Priority string    `json:"priority"`
	Since    time.Time `json:"since"`
}

type WaitingPageBody struct {
	Tickets []WaitingTicketBody `json:"tickets"`
	// Total is how many tickets in the status have a recorded time, of which
	// Tickets is one page.
	Total int64 `json:"total"`
}

type GetWaitingRequest struct {
	Status   string `query:"status" enum:"preparing,parked,open,in_progress,in_merge_queue,blocked,discussion,review"`
	Priority string `query:"priority" enum:"low,normal,high,urgent"`
	// IncludeParked must be set to ask for the parked status.
	IncludeParked bool `query:"include_parked" default:"false"`
	Limit         int  `query:"limit" default:"10" minimum:"1" maximum:"50"`
	// Offset is capped: each skipped ticket still costs a lookup, and nothing
	// legitimate pages past a few hundred waiting tickets.
	Offset int `query:"offset" default:"0" minimum:"0" maximum:"1000"`
}

type GetWaitingResponse struct {
	Body *WaitingPageBody
}

func (h *Handler) GetWaiting(ctx context.Context, in *GetWaitingRequest) (*GetWaitingResponse, error) {
	page, err := h.svc.Waiting(ctx, in.Status, in.Priority, in.IncludeParked, in.Limit, in.Offset)
	if err != nil {
		return nil, h.mapError(err)
	}
	body := &WaitingPageBody{Total: page.Total, Tickets: make([]WaitingTicketBody, 0, len(page.Items))}
	for _, w := range page.Items {
		body.Tickets = append(body.Tickets, WaitingTicketBody{ID: w.TicketID, Title: w.Title, Priority: w.Priority, Since: w.Since.UTC()})
	}
	return &GetWaitingResponse{Body: body}, nil
}
