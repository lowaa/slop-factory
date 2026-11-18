package reports

import (
	"context"
	"time"
)

// QuantilesBody is a p50 and p95, in seconds.
type QuantilesBody struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

func quantilesBody(q *Quantiles) *QuantilesBody {
	if q == nil {
		return nil
	}
	return &QuantilesBody{P50: q.P50, P95: q.P95}
}

type LockDayBody struct {
	// Day is the UTC day, absent on the period's total.
	Day *time.Time `json:"day"`
	// Holds were acquired in it; Ongoing of them are held still.
	Holds   int64 `json:"holds"`
	Ongoing int64 `json:"ongoing"`
	// Hold is over the released holds, acquired to released; absent when
	// none is.
	Hold *QuantilesBody `json:"hold"`
	// Queued of the holds waited first; Wait is queued to acquired over
	// those, absent when none did.
	Queued int64          `json:"queued"`
	Wait   *QuantilesBody `json:"wait"`
}

type LockOverlapBody struct {
	Depth int `json:"depth"`
	// Seconds is change-seconds spent waiting during a hold, at most Depth
	// changes at a time.
	Seconds float64 `json:"seconds"`
}

// LockHistoryBody is the integration lock section, over [from, to).
type LockHistoryBody struct {
	From        time.Time         `json:"from"`
	To          time.Time         `json:"to"`
	Total       LockDayBody       `json:"total"`
	Days        []LockDayBody     `json:"days"`
	HeldSeconds float64           `json:"held_seconds"`
	Overlap     []LockOverlapBody `json:"overlap"`
}

func lockDayBody(d *LockDay, withDay bool) LockDayBody {
	b := LockDayBody{Holds: d.Holds, Ongoing: d.Ongoing, Hold: quantilesBody(d.Hold), Queued: d.Queued, Wait: quantilesBody(d.Wait)}
	if withDay {
		day := d.Day.UTC()
		b.Day = &day
	}
	return b
}

func lockHistoryBody(h *LockHistory) *LockHistoryBody {
	b := &LockHistoryBody{From: h.From.UTC(), To: h.To.UTC(), Total: lockDayBody(&h.Total, false), HeldSeconds: h.HeldSeconds,
		Days: make([]LockDayBody, 0, len(h.Days)), Overlap: make([]LockOverlapBody, 0, len(h.Overlap))}
	for i := range h.Days {
		b.Days = append(b.Days, lockDayBody(&h.Days[i], true))
	}
	for _, o := range h.Overlap {
		b.Overlap = append(b.Overlap, LockOverlapBody{Depth: o.Depth, Seconds: o.Seconds})
	}
	return b
}

type GetLockHistoryRequest struct {
	// Days is the length of the period, ending now.
	Days int `query:"days" default:"30" enum:"7,30"`
}

type GetLockHistoryResponse struct {
	Body *LockHistoryBody
}

func (h *Handler) GetLockHistory(ctx context.Context, in *GetLockHistoryRequest) (*GetLockHistoryResponse, error) {
	r, err := h.svc.LockHistory(ctx, in.Days)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &GetLockHistoryResponse{Body: lockHistoryBody(r)}, nil
}
