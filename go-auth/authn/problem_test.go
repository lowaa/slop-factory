package authn_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/go-auth/authn"
)

var (
	errA     = errors.New("user: first thing")
	errB     = errors.New("erasure: second thing")
	errPlain = errors.New("no prefix here")
	errBase  = errors.New("user: base")
	errWrap  = fmt.Errorf("user: specific: %w", errBase)
)

func TestProblemTable_ProblemFor(t *testing.T) {
	table := authn.NewProblemTable([]string{"user: ", "erasure: "},
		authn.ProblemEntry{Err: errA, Status: http.StatusForbidden, Code: "a"},
		authn.ProblemEntry{Err: errB, Status: http.StatusConflict, Code: "b"},
		authn.ProblemEntry{Err: errPlain, Status: http.StatusBadRequest, Code: "plain"},
		authn.ProblemEntry{Err: errWrap, Status: http.StatusTeapot, Code: "specific"},
		authn.ProblemEntry{Err: errBase, Status: http.StatusGone, Code: "base"},
	)

	for _, tc := range []struct {
		name string
		err  error
		want *authn.Problem
	}{
		{"strips the first configured prefix", errA, authn.NewProblem(http.StatusForbidden, "a", "first thing")},
		{"strips another configured prefix", errB, authn.NewProblem(http.StatusConflict, "b", "second thing")},
		{"keeps text with no configured prefix", errPlain, authn.NewProblem(http.StatusBadRequest, "plain", "no prefix here")},
		{"matches a wrapped error", fmt.Errorf("handler: %w", errA), authn.NewProblem(http.StatusForbidden, "a", "first thing")},
		{"matches through errors.Join", errors.Join(errors.New("other"), errB), authn.NewProblem(http.StatusConflict, "b", "second thing")},
		{"first entry wins over a later one it also matches", errWrap, authn.NewProblem(http.StatusTeapot, "specific", "specific: user: base")},
		{"unmatched is nil", errors.New("pq: dial postgres://u:hunter2@db/x"), nil},
		{"nil is nil", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, table.ProblemFor(tc.err))
		})
	}
}

func TestProblemTable_DetailIsTheSentinelsNotTheWrappers(t *testing.T) {
	table := authn.NewProblemTable([]string{"user: "}, authn.ProblemEntry{Err: errA, Status: 403, Code: "a"})
	leaky := fmt.Errorf("query postgres://u:hunter2@db/x failed: %w", errA)

	p := table.ProblemFor(leaky)

	assert.Equal(t, "first thing", p.Detail)
	assert.NotContains(t, p.Detail, "hunter2")
}

func TestProblemTable_NoPrefixesLeavesDetailAlone(t *testing.T) {
	table := authn.NewProblemTable(nil, authn.ProblemEntry{Err: errA, Status: 403, Code: "a"})
	assert.Equal(t, "user: first thing", table.ProblemFor(errA).Detail)
}
