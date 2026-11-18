package dsr

import "time"

// Period is a length of time in calendar units: a month is not a fixed
// number of days, and GDPR's deadline is a month.
type Period struct {
	Months int
	Days   int
}

func (p Period) add(q Period) Period { return Period{p.Months + q.Months, p.Days + q.Days} }

// Rule is one regime's clock: the time to respond, and the further time the
// regime allows when a request is complex or numerous.
type Rule struct {
	Base      Period
	Extension Period
}

// Deadlines is the deadline configuration. The defaults are PLACEHOLDERS
// that a qualified person has to confirm: this is software configuration,
// not a statement of what the law requires.
type Deadlines struct {
	GDPR Rule
	CCPA Rule
}

// DefaultDeadlines is a month, extendable by two, for GDPR, and 45 days,
// extendable by 45, for CCPA.
func DefaultDeadlines() *Deadlines {
	return &Deadlines{
		GDPR: Rule{Base: Period{Months: 1}, Extension: Period{Months: 2}},
		CCPA: Rule{Base: Period{Days: 45}, Extension: Period{Days: 45}},
	}
}

func (d *Deadlines) rule(r Regime) Rule {
	if r == RegimeCCPA {
		return d.CCPA
	}
	return d.GDPR
}

// Due is when a request of type t, received at received, has to be answered
// by. An extension is added to the base period before the date is worked
// out, so an extended deadline is never an end-of-month clamp applied twice.
func (d *Deadlines) Due(t Type, received time.Time, extended bool) time.Time {
	rule := d.rule(t.Regime())
	p := rule.Base
	if extended {
		p = p.add(rule.Extension)
	}
	return addPeriod(received.UTC(), p)
}

// addPeriod adds whole calendar months, then days. A month added to the
// 31st lands on the last day of a shorter month, not in the next one, as
// "the corresponding date in the following month" reads.
func addPeriod(t time.Time, p Period) time.Time {
	year, month, day := t.Date()
	target := time.Date(year, month+time.Month(p.Months), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	last := time.Date(target.Year(), target.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	target = target.AddDate(0, 0, min(day, last)-1)
	return target.AddDate(0, 0, p.Days)
}
