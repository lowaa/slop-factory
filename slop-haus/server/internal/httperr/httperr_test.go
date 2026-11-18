package httperr

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

var (
	errA = errors.New("a")
	errB = errors.New("b")
	errC = errors.New("c")
)

func statusOf(t *testing.T, err error) (int, string) {
	t.Helper()
	var se huma.StatusError
	require.ErrorAs(t, err, &se)
	return se.GetStatus(), err.Error()
}

func TestMapRulesInOrderWithMessages(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	table := &Table{
		Rules: []Rule{
			{Err: errA, Kind: NotFound, Message: "fixed"},
			{Err: errB, Kind: Conflict},
			{Err: errC, Kind: Unavailable, Code: "c_code", Warn: "c happened"},
			SignIn,
		},
		Unexpected: "unexpected thing",
	}

	status, msg := statusOf(t, Map(logger, fmt.Errorf("wrapped: %w", errA), table))
	require.Equal(t, 404, status)
	require.Equal(t, "fixed", msg)

	status, msg = statusOf(t, Map(logger, errB, table))
	require.Equal(t, 409, status)
	require.Equal(t, "b", msg)

	out := Map(logger, errC, table)
	var p *authn.Problem
	require.ErrorAs(t, out, &p)
	require.Equal(t, "c_code", p.Code)
	require.Equal(t, 503, p.GetStatus())
	require.Contains(t, buf.String(), "c happened")

	status, msg = statusOf(t, Map(logger, authn.ErrNoUser, table))
	require.Equal(t, 401, status)
	require.Equal(t, "sign in first", msg)
}

func TestMapUnclaimedIsALogged500(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	status, msg := statusOf(t, Map(logger, errors.New("secret"), &Table{Unexpected: "unexpected thing"}))
	require.Equal(t, 500, status)
	require.Equal(t, "internal server error", msg)
	require.Contains(t, buf.String(), "unexpected thing")
	require.Contains(t, buf.String(), "secret")
}

func TestCustomClaimsFirstAndCountsAsCovered(t *testing.T) {
	custom := huma.Error418Teapot("tea")
	table := &Table{
		Rules: []Rule{{Err: errA, Kind: NotFound}},
		Custom: func(err error) error {
			if errors.Is(err, errB) {
				return custom
			}
			return nil
		},
	}
	require.Equal(t, custom, Map(slog.Default(), errB, table))
	require.True(t, table.Covers(errA))
	require.True(t, table.Covers(errB))
	require.False(t, table.Covers(errC))
}

func TestFirstMatchingRuleWins(t *testing.T) {
	both := fmt.Errorf("%w and %w", errA, errB)
	table := &Table{Rules: []Rule{
		{Err: errA, Kind: NotFound},
		{Err: errB, Kind: Conflict},
	}}
	status, _ := statusOf(t, Map(slog.Default(), both, table))
	require.Equal(t, 404, status)

	table.Rules[0], table.Rules[1] = table.Rules[1], table.Rules[0]
	status, _ = statusOf(t, Map(slog.Default(), both, table))
	require.Equal(t, 409, status)
}
