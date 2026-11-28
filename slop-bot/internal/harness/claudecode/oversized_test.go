package claudecode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// A result too long to read: its outcome comes before its text.
func TestAnOversizedResultStillEndsTheTurn(t *testing.T) {
	t.Parallel()
	c := &codec{}
	decodeAll(t, c, `{"type":"system","subtype":"init"}`)
	evs, err := c.Oversized([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"an enormous answ`), nil)
	require.NoError(t, err)
	require.NotEmpty(t, evs)
	ended := evs[len(evs)-1]
	assert.Equal(t, harness.TurnEnded, ended.Kind)
	assert.Equal(t, harness.TurnCompleted, ended.Outcome)
}

// An oversized record with no field whole before the cut says nothing.
func TestAnOversizedRecordWithNothingWholeSaysNothing(t *testing.T) {
	t.Parallel()
	evs, err := (&codec{}).Oversized([]byte(`{"type":"resu`), nil)
	require.NoError(t, err)
	assert.Empty(t, evs)
}
