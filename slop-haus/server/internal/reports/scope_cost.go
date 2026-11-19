package reports

import (
	"cmp"
	"context"
	"slices"
	"time"
)

// Scope cost: how much exclusive path scopes hold the fleet back, over
// scope_blocks, scope_extension_denials and scope_narrowings (written by the
// ticket package at the claim and scope decisions).
//
// How a figure is counted:
//
//   - A block is an interval, clipped to the period [from, to). One still
//     open counts up to the period's end and is reported as ongoing -- it is
//     never read as zero.
//   - A ticket's wait is the union of its block intervals, so two scopes
//     holding it back at once are not counted twice. Per blocking ticket or
//     path, the figure sums those tickets' unions: ticket-seconds.
//   - Idle capacity is time with a worker that exists and holds no lease
//     while some ticket is held back. Workers are taken as existing from
//     their creation until they are disabled, and a re-enabled worker's gap
//     is unknown, so it is counted as existing; time with ticket processing
//     switched off is not excluded either.
//   - An extension denial ends when the agent is granted the path
//     ("granted"), narrows ("narrowed") or its lease ends ("gave_up"); one
//     with none of these is open.
//   - Overlaps that a shared path would not block are not separated: no
//     scope is shared yet.

// TopScopeItems caps each ranked list.
const TopScopeItems = 10

// Span is a half-open interval; a nil End is still going.
type Span struct {
	Start time.Time
	End   *time.Time
}

// ScopeBlock is one interval a ticket was held back by one scope.
type ScopeBlock struct {
	Span
	TicketID     int64
	TicketTitle  string
	BlockerID    int64
	BlockerTitle string
	Path         string
	// EndReason is "claimed" or "cleared"; empty while open.
	EndReason string
}

// Ways a refused extension ends.
const (
	DenialGranted  = "granted"
	DenialNarrowed = "narrowed"
	DenialGaveUp   = "gave_up"
)

// ScopeDenial is one refused scope extension.
type ScopeDenial struct {
	TicketID     int64
	TicketTitle  string
	Path         string
	BlockerID    int64
	BlockingPath string
	DeniedAt     time.Time
	// ResolvedAt and Resolution are unset while it is open.
	ResolvedAt *time.Time
	Resolution string
}

// ScopeCostInput is the rows a scope cost report is computed from.
type ScopeCostInput struct {
	From, To time.Time
	// Blocks overlap the period; Denials are those denied in it.
	Blocks     []ScopeBlock
	Denials    []ScopeDenial
	Narrowings int64
	// Workers are the spans worker agents exist; Busy the spans they hold a
	// lease.
	Workers []Span
	Busy    []Span
	// LockingOff is whether the tenant has scope locking off now: no block
	// is recorded while it is, so an empty report is not a clean one.
	LockingOff bool
}

// ScopeWait is one ticket's time held back.
type ScopeWait struct {
	TicketID   int64
	Title      string
	Seconds    float64
	Ongoing    bool
	Claimed    bool
	BlockerIDs []int64
}

// ScopeBlockerCost is what one blocking ticket held back.
type ScopeBlockerCost struct {
	TicketID int64
	Title    string
	// Seconds is ticket-seconds; Tickets how many were held back by it.
	Seconds float64
	Tickets int64
}

// ScopePathCost is what one held path held back.
type ScopePathCost struct {
	Path    string
	Seconds float64
	Tickets int64
}

// ScopeWaits is the time tickets spent held back.
type ScopeWaits struct {
	// Tickets were held back at some point in the period; Claimed of them
	// were claimed after it, Ongoing are held back still.
	Tickets, Claimed, Ongoing int64
	TotalSeconds              float64
	// MedianSeconds is absent when no ticket was held back.
	MedianSeconds *float64
}

// ScopeDenials is the extensions refused.
type ScopeDenials struct {
	Total, Granted, Narrowed, GaveUp, Open int64
	// MedianSecondsToResolve is over the resolved ones; absent when none is.
	MedianSecondsToResolve *float64
	Paths                  []ScopePathCount
}

type ScopePathCount struct {
	Path  string
	Count int64
}

// ScopeCost is the report.
type ScopeCost struct {
	From, To time.Time
	// Intervals is how many times a ticket became held back in the period.
	Intervals int64
	Waits     ScopeWaits
	// BlockedSeconds is time with any ticket held back; IdleBlockedSeconds
	// the part of it with an idle worker: the capacity scopes cost.
	BlockedSeconds     float64
	IdleBlockedSeconds float64
	Tickets            []ScopeWait
	Blockers           []ScopeBlockerCost
	Paths              []ScopePathCost
	Denials            ScopeDenials
	Narrowings         int64
	LockingOff         bool
}

type interval struct{ start, end time.Time }

func (in *ScopeCostInput) clip(s Span) (interval, bool) {
	iv := interval{start: s.Start, end: in.To}
	if s.End != nil && s.End.Before(in.To) {
		iv.end = *s.End
	}
	if iv.start.Before(in.From) {
		iv.start = in.From
	}
	return iv, iv.end.After(iv.start)
}

// unionSeconds is the time covered by ivs, overlaps counted once.
func unionSeconds(ivs []interval) float64 {
	slices.SortFunc(ivs, func(a, b interval) int { return a.start.Compare(b.start) })
	var total time.Duration
	var cur *interval
	for i := range ivs {
		if cur == nil || ivs[i].start.After(cur.end) {
			if cur != nil {
				total += cur.end.Sub(cur.start)
			}
			c := ivs[i]
			cur = &c
			continue
		}
		if ivs[i].end.After(cur.end) {
			cur.end = ivs[i].end
		}
	}
	if cur != nil {
		total += cur.end.Sub(cur.start)
	}
	return total.Seconds()
}

func median(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	slices.Sort(v)
	m := v[len(v)/2]
	if len(v)%2 == 0 {
		m = (v[len(v)/2-1] + v[len(v)/2]) / 2
	}
	return &m
}

// ComputeScopeCost works the report out of rows. It is a function of its
// input alone, so the arithmetic is tested over fixed rows.
func ComputeScopeCost(in *ScopeCostInput) *ScopeCost {
	out := &ScopeCost{From: in.From, To: in.To, Narrowings: in.Narrowings, LockingOff: in.LockingOff,
		Tickets: []ScopeWait{}, Blockers: []ScopeBlockerCost{}, Paths: []ScopePathCost{}}

	type group struct {
		title string
		ivs   map[int64][]interval
	}
	byTicket := map[int64]*ScopeWait{}
	ticketIvs := map[int64][]interval{}
	byBlocker := map[int64]*group{}
	byPath := map[string]*group{}
	var blocked []interval

	add := func(g map[int64][]interval, ticket int64, iv interval) { g[ticket] = append(g[ticket], iv) }
	for i := range in.Blocks {
		b := &in.Blocks[i]
		iv, ok := in.clip(b.Span)
		if !ok {
			continue
		}
		if !b.Start.Before(in.From) {
			out.Intervals++
		}
		blocked = append(blocked, iv)
		w := byTicket[b.TicketID]
		if w == nil {
			w = &ScopeWait{TicketID: b.TicketID, Title: b.TicketTitle}
			byTicket[b.TicketID] = w
		}
		w.Ongoing = w.Ongoing || b.End == nil
		w.Claimed = w.Claimed || b.EndReason == "claimed"
		if !slices.Contains(w.BlockerIDs, b.BlockerID) {
			w.BlockerIDs = append(w.BlockerIDs, b.BlockerID)
		}
		ticketIvs[b.TicketID] = append(ticketIvs[b.TicketID], iv)
		bg := byBlocker[b.BlockerID]
		if bg == nil {
			bg = &group{title: b.BlockerTitle, ivs: map[int64][]interval{}}
			byBlocker[b.BlockerID] = bg
		}
		add(bg.ivs, b.TicketID, iv)
		pg := byPath[b.Path]
		if pg == nil {
			pg = &group{ivs: map[int64][]interval{}}
			byPath[b.Path] = pg
		}
		add(pg.ivs, b.TicketID, iv)
	}

	var waits []float64
	for id, w := range byTicket {
		w.Seconds = unionSeconds(ticketIvs[id])
		slices.Sort(w.BlockerIDs)
		out.Waits.Tickets++
		out.Waits.TotalSeconds += w.Seconds
		if w.Ongoing {
			out.Waits.Ongoing++
		}
		if w.Claimed {
			out.Waits.Claimed++
		}
		waits = append(waits, w.Seconds)
		out.Tickets = append(out.Tickets, *w)
	}
	out.Waits.MedianSeconds = median(waits)
	slices.SortFunc(out.Tickets, func(a, b ScopeWait) int {
		return cmp.Or(cmp.Compare(b.Seconds, a.Seconds), cmp.Compare(a.TicketID, b.TicketID))
	})
	out.Tickets = out.Tickets[:min(len(out.Tickets), TopScopeItems)]

	sum := func(g *group) (seconds float64) {
		for _, ivs := range g.ivs {
			seconds += unionSeconds(ivs)
		}
		return seconds
	}
	for id, g := range byBlocker {
		out.Blockers = append(out.Blockers, ScopeBlockerCost{TicketID: id, Title: g.title, Seconds: sum(g), Tickets: int64(len(g.ivs))})
	}
	slices.SortFunc(out.Blockers, func(a, b ScopeBlockerCost) int {
		return cmp.Or(cmp.Compare(b.Seconds, a.Seconds), cmp.Compare(a.TicketID, b.TicketID))
	})
	out.Blockers = out.Blockers[:min(len(out.Blockers), TopScopeItems)]
	for path, g := range byPath {
		out.Paths = append(out.Paths, ScopePathCost{Path: path, Seconds: sum(g), Tickets: int64(len(g.ivs))})
	}
	slices.SortFunc(out.Paths, func(a, b ScopePathCost) int {
		return cmp.Or(cmp.Compare(b.Seconds, a.Seconds), cmp.Compare(a.Path, b.Path))
	})
	out.Paths = out.Paths[:min(len(out.Paths), TopScopeItems)]

	out.BlockedSeconds = unionSeconds(slices.Clone(blocked))
	out.IdleBlockedSeconds = in.idleWhileBlocked(blocked)
	out.Denials = in.denials()
	return out
}

// idleWhileBlocked sweeps the period for time with a worker that holds no
// lease while some ticket is held back.
func (in *ScopeCostInput) idleWhileBlocked(blocked []interval) float64 {
	type event struct {
		at                    time.Time
		workers, busy, blocks int
	}
	var events []event
	for _, s := range in.Workers {
		if iv, ok := in.clip(s); ok {
			events = append(events, event{at: iv.start, workers: 1}, event{at: iv.end, workers: -1})
		}
	}
	for _, s := range in.Busy {
		if iv, ok := in.clip(s); ok {
			events = append(events, event{at: iv.start, busy: 1}, event{at: iv.end, busy: -1})
		}
	}
	for _, iv := range blocked {
		events = append(events, event{at: iv.start, blocks: 1}, event{at: iv.end, blocks: -1})
	}
	slices.SortStableFunc(events, func(a, b event) int { return a.at.Compare(b.at) })

	var workers, busy, blocks int
	var idle time.Duration
	for i, e := range events {
		workers, busy, blocks = workers+e.workers, busy+e.busy, blocks+e.blocks
		if i+1 < len(events) && blocks > 0 && workers > busy {
			idle += events[i+1].at.Sub(e.at)
		}
	}
	return idle.Seconds()
}

func (in *ScopeCostInput) denials() ScopeDenials {
	d := ScopeDenials{Paths: []ScopePathCount{}}
	counts := map[string]int64{}
	var secs []float64
	for i := range in.Denials {
		x := &in.Denials[i]
		d.Total++
		counts[x.Path]++
		switch {
		case x.ResolvedAt == nil:
			d.Open++
			continue
		case x.Resolution == DenialGranted:
			d.Granted++
		case x.Resolution == DenialNarrowed:
			d.Narrowed++
		default:
			d.GaveUp++
		}
		secs = append(secs, x.ResolvedAt.Sub(x.DeniedAt).Seconds())
	}
	d.MedianSecondsToResolve = median(secs)
	for p, n := range counts {
		d.Paths = append(d.Paths, ScopePathCount{Path: p, Count: n})
	}
	slices.SortFunc(d.Paths, func(a, b ScopePathCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Path, b.Path))
	})
	d.Paths = d.Paths[:min(len(d.Paths), TopScopeItems)]
	return d
}

// ScopeCost reports the last days days, ending now.
func (s *Service) ScopeCost(ctx context.Context, days int) (*ScopeCost, error) {
	f, err := s.usagePeriod(days)
	if err != nil {
		return nil, err
	}
	in, err := s.store.ScopeCostRows(ctx, f.From, f.To)
	if err != nil {
		return nil, err
	}
	return ComputeScopeCost(in), nil
}
