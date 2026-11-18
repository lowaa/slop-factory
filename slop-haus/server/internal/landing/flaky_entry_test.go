package landing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlakyEntry_ParsesTheOwnedFormAndABareName(t *testing.T) {
	got, err := parseKnownFlaky([]string{"slop-haus-test-e2e:1566:2026-11-22", "legacy"})
	require.NoError(t, err)
	assert.Equal(t, flakyEntry{Name: "slop-haus-test-e2e", Owner: 1566, Review: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)}, got[0])
	assert.True(t, got[1].bare())
	assert.False(t, got[0].bare())
}

func TestFlakyEntry_ExpiresTheDayAfterItsReviewDate(t *testing.T) {
	e := flakyEntry{Name: "w", Owner: 1, Review: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)}
	assert.False(t, e.expired(time.Date(2026, 11, 22, 23, 59, 0, 0, time.UTC)), "the review day itself is still in date")
	assert.True(t, e.expired(time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)))
	assert.False(t, flakyEntry{Name: "w"}.expired(time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)), "a bare name never expires")
}
