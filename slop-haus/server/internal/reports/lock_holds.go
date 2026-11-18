package reports

import (
	"context"
	"math"
	"slices"
	"time"
)

// Integration lock history: how long the lock is held and how long agents
// queue for it, over lock_holds (written by triggers on locks).
//
// How a figure is counted:
//
//   - A hold belongs to the UTC day it was acquired on, and to the period if
//     that is in [from, to). Hold time needs the release, so a hold still
//     open is counted as ongoing and left out of the hold percentiles; its
//     wait is known, so it is in the wait percentiles.
//   - Wait is queued_at to acquired_at, over the holds that queued. An agent
//     that found the lock free took it at once and has no wait, and an agent
//     that queued and left (its lease ended) never got a hold, so is not
//     recorded: the waits are those of agents that got the lock.
//   - Percentiles interpolate linearly between the two nearest values.
//   - A queue of depth N lets up to N waiting changes prepare their merge
//     (merge main, test, wait for checks) while the lock is held, instead of
//     after it is free. HoldOverlap is the estimate of that preparation: for
//     each depth, the change-seconds that waited during a hold, counting at
//     most N waiting changes at any moment. Depth 1 is the time a hold had
//     someone waiting behind it, the most wall-clock time one queued change
//     could take off the next hold. It is an upper bound: it assumes the
//     waiting work could have been done early, and that handing the lock on
//     costs nothing.

// OverlapDepths are the queue depths the overlap is estimated for.
var OverlapDepths = []int{1, 2, 3}

// LockHold is one hold of a lock.
type LockHold struct {
	Name     string
	TicketID int64
	// QueuedAt is nil when the lock was free and taken at once.
	QueuedAt   *time.Time
	AcquiredAt time.Time
	// ReleasedAt is nil while it is held.
	ReleasedAt *time.Time
}

// LockHistoryInput is the rows a lock history report is computed from.
type LockHistoryInput struct {
	From, To time.Time
	// Holds overlap the period: acquired before its end and not released
	// before its start. Those released before the period began matter only
	// as time the lock was held.
	Holds []LockHold
}

// Quantiles is a p50 and p95 in seconds.
type Quantiles struct{ P50, P95 float64 }

// LockDay is one UTC day's holds and waits.
type LockDay struct {
	Day time.Time
	// Holds were acquired on the day; Ongoing of them are held still.
	Holds, Ongoing int64
	// Hold is over the released ones; nil when none is.
	Hold *Quantiles
	// Queued of the holds waited first; Wait is over those, nil when none.
	Queued int64
	Wait   *Quantiles
}

// OverlapEstimate is what a queue of Depth could have overlapped with holds.
type OverlapEstimate struct {
	Depth   int
	Seconds float64
}

// LockHistory is the report.
type LockHistory struct {
	From, To time.Time
	Total    LockDay
	Days     []LockDay
	// HeldSeconds is time the lock was held in the period.
	HeldSeconds float64
	Overlap     []OverlapEstimate
}

func quantiles(v []float64) *Quantiles {
	if len(v) == 0 {
		return nil
	}
	slices.Sort(v)
	return &Quantiles{P50: percentile(v, 0.5), P95: percentile(v, 0.95)}
}

// percentile of sorted, non-empty values, interpolating linearly.
func percentile(sorted []float64, p float64) float64 {
	rank := p * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := min(lo+1, len(sorted)-1)
	return sorted[lo] + (sorted[hi]-sorted[lo])*(rank-float64(lo))
}

type dayFigures struct {
	holds, ongoing, queued int64
	hold, wait             []float64
}

func (d *dayFigures) add(h *LockHold) {
	d.holds++
	if h.QueuedAt != nil {
		d.queued++
		d.wait = append(d.wait, h.AcquiredAt.Sub(*h.QueuedAt).Seconds())
	}
	if h.ReleasedAt == nil {
		d.ongoing++
		return
	}
	d.hold = append(d.hold, h.ReleasedAt.Sub(h.AcquiredAt).Seconds())
}

func (d *dayFigures) lockDay(day time.Time) LockDay {
	return LockDay{Day: day, Holds: d.holds, Ongoing: d.ongoing, Hold: quantiles(d.hold), Queued: d.queued, Wait: quantiles(d.wait)}
}

// ComputeLockHistory works the report out of rows. It is a function of its
// input alone, so the arithmetic is tested over fixed rows.
func ComputeLockHistory(in *LockHistoryInput) *LockHistory {
	out := &LockHistory{From: in.From, To: in.To, Days: []LockDay{}}

	var total dayFigures
	byDay := map[time.Time]*dayFigures{}
	for i := range in.Holds {
		h := &in.Holds[i]
		if h.AcquiredAt.Before(in.From) || !h.AcquiredAt.Before(in.To) {
			continue
		}
		day := h.AcquiredAt.UTC().Truncate(24 * time.Hour)
		d := byDay[day]
		if d == nil {
			d = &dayFigures{}
			byDay[day] = d
		}
		d.add(h)
		total.add(h)
	}
	out.Total = total.lockDay(time.Time{})
	for day, d := range byDay {
		out.Days = append(out.Days, d.lockDay(day))
	}
	slices.SortFunc(out.Days, func(a, b LockDay) int { return a.Day.Compare(b.Day) })

	out.HeldSeconds, out.Overlap = in.overlap()
	return out
}

// overlap sweeps the period for the time the lock was held and, within it,
// the number of changes waiting.
func (in *LockHistoryInput) overlap() (heldSeconds float64, est []OverlapEstimate) {
	type event struct {
		at            time.Time
		held, waiting int
	}
	clip := func(start time.Time, end *time.Time) (time.Time, time.Time, bool) {
		e := in.To
		if end != nil && end.Before(e) {
			e = *end
		}
		s := start
		if s.Before(in.From) {
			s = in.From
		}
		return s, e, e.After(s)
	}
	var events []event
	for i := range in.Holds {
		h := &in.Holds[i]
		if s, e, ok := clip(h.AcquiredAt, h.ReleasedAt); ok {
			events = append(events, event{at: s, held: 1}, event{at: e, held: -1})
		}
		if h.QueuedAt == nil {
			continue
		}
		if s, e, ok := clip(*h.QueuedAt, &h.AcquiredAt); ok {
			events = append(events, event{at: s, waiting: 1}, event{at: e, waiting: -1})
		}
	}
	slices.SortStableFunc(events, func(a, b event) int { return a.at.Compare(b.at) })

	est = make([]OverlapEstimate, len(OverlapDepths))
	for i, n := range OverlapDepths {
		est[i].Depth = n
	}
	var held, waiting int
	for i, e := range events {
		held, waiting = held+e.held, waiting+e.waiting
		if i+1 == len(events) || held == 0 {
			continue
		}
		span := events[i+1].at.Sub(e.at).Seconds()
		heldSeconds += span
		for j, n := range OverlapDepths {
			est[j].Seconds += span * float64(min(waiting, n))
		}
	}
	return heldSeconds, est
}

// LockHistory reports the last days days, ending now.
func (s *Service) LockHistory(ctx context.Context, days int) (*LockHistory, error) {
	f, err := s.usagePeriod(days)
	if err != nil {
		return nil, err
	}
	in, err := s.store.LockHistoryRows(ctx, f.From, f.To)
	if err != nil {
		return nil, err
	}
	return ComputeLockHistory(in), nil
}
