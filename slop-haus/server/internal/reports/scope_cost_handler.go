package reports

import (
	"context"
	"time"
)

type ScopeWaitBody struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	// Seconds is the time held back in the period; for an ongoing wait,
	// up to the period's end.
	Seconds float64 `json:"seconds"`
	Ongoing bool    `json:"ongoing"`
	Claimed bool    `json:"claimed"`
	// BlockerIDs are the tickets whose scopes held it back.
	BlockerIDs []int64 `json:"blocker_ids"`
}

type ScopeBlockerBody struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	// Seconds is ticket-seconds: each ticket it held back, summed.
	Seconds float64 `json:"seconds"`
	Tickets int64   `json:"tickets"`
}

type ScopePathBody struct {
	Path    string  `json:"path"`
	Seconds float64 `json:"seconds"`
	Tickets int64   `json:"tickets"`
}

type ScopeWaitsBody struct {
	Tickets int64 `json:"tickets"`
	Claimed int64 `json:"claimed"`
	Ongoing int64 `json:"ongoing"`
	// TotalSeconds sums each ticket's wait; MedianSeconds is absent when no
	// ticket was held back.
	TotalSeconds  float64  `json:"total_seconds"`
	MedianSeconds *float64 `json:"median_seconds"`
}

type ScopePathCountBody struct {
	Path  string `json:"path"`
	Count int64  `json:"count"`
}

type ScopeDenialsBody struct {
	Total    int64 `json:"total"`
	Granted  int64 `json:"granted"`
	Narrowed int64 `json:"narrowed"`
	GaveUp   int64 `json:"gave_up"`
	Open     int64 `json:"open"`
	// MedianSecondsToResolve is over the resolved ones; absent when none is.
	MedianSecondsToResolve *float64             `json:"median_seconds_to_resolve"`
	Paths                  []ScopePathCountBody `json:"paths"`
}

// ScopeCostBody is the scope cost section, over [from, to).
type ScopeCostBody struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Intervals is how many times a ticket became held back in the period.
	Intervals int64          `json:"intervals"`
	Waits     ScopeWaitsBody `json:"waits"`
	// BlockedSeconds is time with any ticket held back; IdleBlockedSeconds
	// the part of it with an idle worker.
	BlockedSeconds     float64            `json:"blocked_seconds"`
	IdleBlockedSeconds float64            `json:"idle_blocked_seconds"`
	Tickets            []ScopeWaitBody    `json:"tickets"`
	Blockers           []ScopeBlockerBody `json:"blockers"`
	Paths              []ScopePathBody    `json:"paths"`
	Denials            ScopeDenialsBody   `json:"denials"`
	Narrowings         int64              `json:"narrowings"`
}

func scopeCostBody(c *ScopeCost) *ScopeCostBody {
	b := &ScopeCostBody{
		From: c.From.UTC(), To: c.To.UTC(), Intervals: c.Intervals,
		Waits: ScopeWaitsBody{Tickets: c.Waits.Tickets, Claimed: c.Waits.Claimed, Ongoing: c.Waits.Ongoing,
			TotalSeconds: c.Waits.TotalSeconds, MedianSeconds: c.Waits.MedianSeconds},
		BlockedSeconds: c.BlockedSeconds, IdleBlockedSeconds: c.IdleBlockedSeconds,
		Tickets:  make([]ScopeWaitBody, 0, len(c.Tickets)),
		Blockers: make([]ScopeBlockerBody, 0, len(c.Blockers)),
		Paths:    make([]ScopePathBody, 0, len(c.Paths)),
		Denials: ScopeDenialsBody{Total: c.Denials.Total, Granted: c.Denials.Granted, Narrowed: c.Denials.Narrowed,
			GaveUp: c.Denials.GaveUp, Open: c.Denials.Open, MedianSecondsToResolve: c.Denials.MedianSecondsToResolve,
			Paths: make([]ScopePathCountBody, 0, len(c.Denials.Paths))},
		Narrowings: c.Narrowings,
	}
	for _, t := range c.Tickets {
		b.Tickets = append(b.Tickets, ScopeWaitBody{ID: t.TicketID, Title: t.Title, Seconds: t.Seconds,
			Ongoing: t.Ongoing, Claimed: t.Claimed, BlockerIDs: t.BlockerIDs})
	}
	for _, x := range c.Blockers {
		b.Blockers = append(b.Blockers, ScopeBlockerBody{ID: x.TicketID, Title: x.Title, Seconds: x.Seconds, Tickets: x.Tickets})
	}
	for _, x := range c.Paths {
		b.Paths = append(b.Paths, ScopePathBody{Path: x.Path, Seconds: x.Seconds, Tickets: x.Tickets})
	}
	for _, x := range c.Denials.Paths {
		b.Denials.Paths = append(b.Denials.Paths, ScopePathCountBody{Path: x.Path, Count: x.Count})
	}
	return b
}

type GetScopeCostRequest struct {
	// Days is the length of the period, ending now.
	Days int `query:"days" default:"30" enum:"7,30"`
}

type GetScopeCostResponse struct {
	Body *ScopeCostBody
}

func (h *Handler) GetScopeCost(ctx context.Context, in *GetScopeCostRequest) (*GetScopeCostResponse, error) {
	c, err := h.svc.ScopeCost(ctx, in.Days)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &GetScopeCostResponse{Body: scopeCostBody(c)}, nil
}
