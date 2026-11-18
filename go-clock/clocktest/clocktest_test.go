package clocktest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/go-clock"
)

var (
	_ clock.Clock = (*Fixed)(nil)
	_ clock.Clock = (*Stepped)(nil)
)

func TestFixedAndStepped(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := NewFixed(start)
	assert.Equal(t, start, f.Now())
	f.Advance(time.Hour)
	assert.Equal(t, start.Add(time.Hour), f.Now())
	f.Set(start)
	assert.Equal(t, start, f.Now())

	s := NewStepped(start, time.Second)
	assert.Equal(t, start, s.Now())
	assert.Equal(t, start.Add(time.Second), s.Now())
}

func TestTimersPanic(t *testing.T) {
	assert.Panics(t, func() { NewFixed(time.Time{}).After(time.Second) })
	assert.Panics(t, func() { NewStepped(time.Time{}, 0).NewTicker(time.Second) })
}
