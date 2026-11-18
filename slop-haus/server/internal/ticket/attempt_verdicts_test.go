package ticket

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeVerdicts struct {
	out map[AttemptRef]*AttemptVerdictBody
}

func (f *fakeVerdicts) AttemptVerdicts(context.Context, []AttemptRef) (map[AttemptRef]*AttemptVerdictBody, error) {
	return f.out, nil
}

func TestFillAttemptVerdictsMapsByAttemptNumberAndLeavesTheRestNull(t *testing.T) {
	t.Parallel()
	fake := &fakeVerdicts{out: map[AttemptRef]*AttemptVerdictBody{
		{TicketID: 5, Number: 2}: {AttemptVerdictEntry: AttemptVerdictEntry{Verdict: "would_merge"}},
	}}
	attempts := []AttemptBody{{Number: 1}, {Number: 2}}

	require.NoError(t, fillAttemptVerdicts(context.Background(), fake, 5, attempts))

	assert.Nil(t, attempts[0].Verdict)
	require.NotNil(t, attempts[1].Verdict)
	assert.Equal(t, "would_merge", attempts[1].Verdict.Verdict)
}

func TestFillAttemptVerdictsWithoutAReaderLeavesThemNull(t *testing.T) {
	t.Parallel()
	attempts := []AttemptBody{{Number: 1}}
	require.NoError(t, fillAttemptVerdicts(context.Background(), nil, 5, attempts))
	assert.Nil(t, attempts[0].Verdict)
}
