package dsr

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 9, 30, 0, 0, time.UTC) }

func TestDue(t *testing.T) {
	d := DefaultDeadlines()
	cases := []struct {
		name     string
		typ      Type
		received time.Time
		extended bool
		want     time.Time
	}{
		{"gdpr is the same date a month on", TypeAccess, day(2026, 3, 10), false, day(2026, 4, 10)},
		{"a month from the 31st lands on the last day of a shorter month", TypeErasure, day(2026, 1, 31), false, day(2026, 2, 28)},
		{"leap February", TypeErasure, day(2028, 1, 31), false, day(2028, 2, 29)},
		{"year end", TypeObjection, day(2026, 12, 15), false, day(2027, 1, 15)},
		{"gdpr extension is two further months from the received date", TypeAccess, day(2026, 3, 10), true, day(2026, 6, 10)},
		{"an extension from the 31st clamps once, not twice", TypeAccess, day(2026, 1, 31), true, day(2026, 4, 30)},
		{"ccpa is 45 days", TypeCCPAKnow, day(2026, 1, 1), false, day(2026, 2, 15)},
		{"ccpa extension is 45 days more", TypeCCPADelete, day(2026, 1, 1), true, day(2026, 4, 1)},
		{"an unspecified request takes the gdpr clock", TypeOther, day(2026, 3, 10), false, day(2026, 4, 10)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.True(t, c.want.Equal(d.Due(c.typ, c.received, c.extended)), "want %s got %s", c.want, d.Due(c.typ, c.received, c.extended))
		})
	}
}

func TestDueFollowsConfig(t *testing.T) {
	d := &Deadlines{GDPR: Rule{Base: Period{Days: 10}, Extension: Period{Days: 5}}, CCPA: Rule{Base: Period{Months: 2}}}
	assert.True(t, day(2026, 3, 11).Equal(d.Due(TypeAccess, day(2026, 3, 1), false)))
	assert.True(t, day(2026, 3, 16).Equal(d.Due(TypeAccess, day(2026, 3, 1), true)))
	assert.True(t, day(2026, 5, 1).Equal(d.Due(TypeCCPAKnow, day(2026, 3, 1), false)))
}

func TestEveryTypeHasARegime(t *testing.T) {
	for _, ty := range Types() {
		assert.Contains(t, []Regime{RegimeGDPR, RegimeCCPA}, ty.Regime(), ty)
	}
}
