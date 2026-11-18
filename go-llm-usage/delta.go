package llmusage

import (
	"errors"
	"sort"
)

// ErrKeyMismatch is returned by Delta for two records of different Keys.
var ErrKeyMismatch = errors.New("llmusage: delta between records of different keys")

// Delta returns new minus old: what an upsert that replaced old's totals
// with new's actually added. A nil old is a first report, so the delta is
// new itself. Fields may come out negative when the totals shrank (a
// transcript cut short); Shrank says so, and such a delta must not reach a
// counter.
//
// The result carries new's identity, times and price version; its Tools
// hold only the tools whose totals changed, and a cost is nil only when
// neither side has one.
func Delta(old, new *Record) (*Record, error) {
	if old == nil {
		return new.Clone(), nil
	}
	if old.Key() != new.Key() {
		return nil, ErrKeyMismatch
	}
	d := new.Clone()
	d.Requests -= old.Requests
	d.InputTokens -= old.InputTokens
	d.OutputTokens -= old.OutputTokens
	d.CacheReadTokens -= old.CacheReadTokens
	d.CacheWriteTokens -= old.CacheWriteTokens
	d.ReasoningTokens -= old.ReasoningTokens
	d.ReportedCostMicros = subCost(new.ReportedCostMicros, old.ReportedCostMicros)
	d.EstimatedCostMicros = subCost(new.EstimatedCostMicros, old.EstimatedCostMicros)
	d.Tools = deltaTools(old.Tools, new.Tools)
	return d, nil
}

func subCost(new, old *int64) *int64 {
	if new == nil && old == nil {
		return nil
	}
	var v int64
	if new != nil {
		v = *new
	}
	if old != nil {
		v -= *old
	}
	return &v
}

func deltaTools(old, new []ToolUsage) []ToolUsage {
	before := make(map[toolKey]*ToolUsage, len(old))
	for i := range old {
		before[old[i].key()] = &old[i]
	}
	var out []ToolUsage
	for i := range new {
		d := new[i]
		if o, ok := before[d.key()]; ok {
			d.Calls -= o.Calls
			d.Errors -= o.Errors
			d.DurationMs -= o.DurationMs
			d.ResultBytes -= o.ResultBytes
			delete(before, d.key())
		}
		if d.Calls != 0 || d.Errors != 0 || d.DurationMs != 0 || d.ResultBytes != 0 {
			out = append(out, d)
		}
	}
	// A tool that vanished from the new totals is a shrink of all of it.
	for _, o := range before {
		out = append(out, ToolUsage{
			Category: o.Category, Tool: o.Tool, Command: o.Command,
			Calls: -o.Calls, Errors: -o.Errors, DurationMs: -o.DurationMs, ResultBytes: -o.ResultBytes,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Command < out[j].Command
	})
	return out
}

// Shrank reports whether any count or cost in a delta is negative.
func (r *Record) Shrank() bool {
	if r.Requests < 0 || r.InputTokens < 0 || r.OutputTokens < 0 || r.CacheReadTokens < 0 ||
		r.CacheWriteTokens < 0 || r.ReasoningTokens < 0 {
		return true
	}
	if (r.ReportedCostMicros != nil && *r.ReportedCostMicros < 0) ||
		(r.EstimatedCostMicros != nil && *r.EstimatedCostMicros < 0) {
		return true
	}
	for i := range r.Tools {
		u := &r.Tools[i]
		if u.Calls < 0 || u.Errors < 0 || u.DurationMs < 0 || u.ResultBytes < 0 {
			return true
		}
	}
	return false
}

// IsZero reports whether a delta adds nothing.
func (r *Record) IsZero() bool {
	zeroCost := func(p *int64) bool { return p == nil || *p == 0 }
	return r.Requests == 0 && r.InputTokens == 0 && r.OutputTokens == 0 && r.CacheReadTokens == 0 &&
		r.CacheWriteTokens == 0 && r.ReasoningTokens == 0 && zeroCost(r.ReportedCostMicros) &&
		zeroCost(r.EstimatedCostMicros) && len(r.Tools) == 0
}
