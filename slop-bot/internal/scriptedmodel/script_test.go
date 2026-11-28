package scriptedmodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestScript(t *testing.T) *script {
	t.Helper()
	s := newScript(anthropicMessages{}.summarise)
	s.replace(&Scenario{Name: "s", Turns: []*Turn{{Text: "one"}, {Text: "two"}}})
	return s
}

func scriptRequest(t *testing.T, tools []any) *Request {
	t.Helper()
	return mustParse(t, anthropicMessages{}, nil, messageBody(tools, false))
}

func TestScriptPlaysTurnsInOrderAndRecordsEachRequest(t *testing.T) {
	t.Parallel()
	s := newTestScript(t)
	for _, want := range []string{"one", "two"} {
		turn, g, err := s.answer(scriptRequest(t, anthropicTools()))
		require.NoError(t, err)
		assert.Nil(t, g)
		assert.Equal(t, want, turn.Text)
	}
	assert.Len(t, s.snapshot().Requests, 2)
	assert.Zero(t, s.remaining())
}

func TestScriptRunningPastTheScriptFailsWithTheRequestInTheMessage(t *testing.T) {
	t.Parallel()
	s := newTestScript(t)
	for range 2 {
		_, _, err := s.answer(scriptRequest(t, anthropicTools()))
		require.NoError(t, err)
	}
	_, _, err := s.answer(scriptRequest(t, anthropicTools()))
	require.ErrorIs(t, err, ErrUnexpectedRequest)
	assert.Contains(t, err.Error(), "only 2 turn(s)")
	assert.Contains(t, err.Error(), "last_message=")
	rec := s.snapshot()
	require.Len(t, rec.Errors, 1)
	assert.Contains(t, err.Error(), rec.Errors[0])
	assert.Len(t, rec.Requests, 3, "a failed request is recorded too")
}

func TestScriptAToolTheHarnessDidNotOfferFails(t *testing.T) {
	t.Parallel()
	s := newTestScript(t)
	s.replace(&Scenario{Name: "s", Turns: []*Turn{{ToolCalls: []*ToolCall{call("mcp__slop-haus__ticket_finish", nil)}}}})
	_, _, err := s.answer(scriptRequest(t, anthropicTools()))
	require.ErrorIs(t, err, ErrUnexpectedRequest)
	assert.Contains(t, err.Error(), "did not offer")
	assert.Equal(t, 1, s.remaining(), "the turn is not used up")
}

func TestScriptNoScenarioFails(t *testing.T) {
	t.Parallel()
	s := newTestScript(t)
	s.replace(nil)
	_, _, err := s.answer(scriptRequest(t, anthropicTools()))
	require.ErrorIs(t, err, ErrUnexpectedRequest)
	assert.Contains(t, err.Error(), "no scenario is loaded")
}

func TestScriptAuxiliaryRequestsAreRecordedButDoNotConsumeTheScript(t *testing.T) {
	t.Parallel()
	s := newTestScript(t)
	turn, g, err := s.answer(scriptRequest(t, nil))
	require.NoError(t, err)
	assert.Nil(t, turn)
	assert.Nil(t, g)
	assert.Equal(t, 2, s.remaining())
	rec := s.snapshot()
	assert.Len(t, rec.Requests, 1)
	assert.Empty(t, rec.Conversation())
}

func TestScriptHandsAGatedTurnItsGate(t *testing.T) {
	t.Parallel()
	s := newTestScript(t)
	s.replace(&Scenario{Name: "s", Turns: []*Turn{{Text: "late", Gate: "g"}}})
	_, g, err := s.answer(scriptRequest(t, anthropicTools()))
	require.NoError(t, err)
	require.NotNil(t, g)
	assert.Same(t, s.gate("g"), g)
}

func TestScriptReplacingTheScenarioOpensItsGatesAndStartsAfresh(t *testing.T) {
	t.Parallel()
	s := newTestScript(t)
	old := s.gate("g")
	s.replace(&Scenario{Name: "next", Turns: []*Turn{{Text: "x"}}})
	select {
	case <-old.opened:
	default:
		t.Fatal("the replaced scenario's gate was not opened")
	}
	assert.NotSame(t, old, s.gate("g"), "the next scenario's gate is a new one")
	select {
	case <-s.gate("g").opened:
		t.Fatal("the new gate starts closed")
	default:
	}
}
