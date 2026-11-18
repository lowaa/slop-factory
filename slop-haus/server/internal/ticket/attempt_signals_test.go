package ticket

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSignals struct {
	got  []AttemptRef
	out  map[AttemptRef]*AttemptSignalsBody
	fail error
}

func (f *fakeSignals) AttemptSignals(_ context.Context, refs []AttemptRef) (map[AttemptRef]*AttemptSignalsBody, error) {
	f.got = refs
	return f.out, f.fail
}

func TestFillAttemptSignalsMapsByAttemptNumberAndLeavesTheRestNull(t *testing.T) {
	t.Parallel()
	fake := &fakeSignals{out: map[AttemptRef]*AttemptSignalsBody{{TicketID: 5, Number: 2}: {HeadSHA: "h"}}}
	attempts := []AttemptBody{{Number: 1}, {Number: 2}}

	require.NoError(t, fillAttemptSignals(context.Background(), fake, 5, attempts))

	assert.Equal(t, []AttemptRef{{TicketID: 5, Number: 1}, {TicketID: 5, Number: 2}}, fake.got)
	assert.Nil(t, attempts[0].Signals)
	require.NotNil(t, attempts[1].Signals)
	assert.Equal(t, "h", attempts[1].Signals.HeadSHA)
}

func TestFillAttemptSignalsWithoutAServiceLeavesThemNull(t *testing.T) {
	t.Parallel()
	attempts := []AttemptBody{{Number: 1}}
	require.NoError(t, fillAttemptSignals(context.Background(), nil, 5, attempts))
	assert.Nil(t, attempts[0].Signals)
}

func TestFillAttemptSignalsPropagatesAReadError(t *testing.T) {
	t.Parallel()
	err := fillAttemptSignals(context.Background(), &fakeSignals{fail: errors.New("x")}, 5, []AttemptBody{{Number: 1}})
	assert.Error(t, err)
}
