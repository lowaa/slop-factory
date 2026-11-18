package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

func TestScopeNarrowing(t *testing.T) {
	t.Parallel()
	shared := scope.NewSharedPaths([]scope.WorkspaceRepo{{
		Path:   "app",
		Shared: []scope.SharedPathRule{{Path: "migrations/", Resolve: "renumber"}},
	}})
	cases := map[string]struct {
		held, changed []string
		shared        *scope.SharedPaths
		remove, keep  []string
	}{
		"a directory becomes the changed files under it": {
			held:    []string{"app/server/cmd/api/", "app/server/internal/ticket/"},
			changed: []string{"app/server/cmd/api/modules.go", "app/server/internal/ticket/a.go", "app/server/internal/ticket/b.go"},
			remove:  []string{"app/server/cmd/api/", "app/server/internal/ticket/"},
			keep:    []string{"app/server/cmd/api/modules.go", "app/server/internal/ticket/a.go", "app/server/internal/ticket/b.go"},
		},
		"an untouched directory is dropped when another path stays": {
			held:    []string{"app/server/cmd/api/", "app/docs/readme.md"},
			changed: []string{"app/docs/readme.md"},
			remove:  []string{"app/server/cmd/api/"},
		},
		"declared files and shared paths stay": {
			held:    []string{"app/migrations/", "app/server/x.go", "app/server/"},
			changed: []string{"app/server/y.go", "app/migrations/1.sql"},
			shared:  shared,
			remove:  []string{"app/server/"},
			keep:    []string{"app/server/y.go"},
		},
		"nothing changed under any path leaves the scope alone": {
			held:    []string{"app/server/"},
			changed: []string{"app/other/z.go"},
		},
		"no change files leaves the scope alone": {
			held: []string{"app/server/"},
		},
		"already narrow": {
			held:    []string{"app/server/x.go"},
			changed: []string{"app/server/x.go"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			remove, keep := ticket.ScopeNarrowing(tc.held, tc.changed, tc.shared)
			assert.Equal(t, tc.remove, remove)
			assert.Equal(t, tc.keep, keep)
		})
	}
}

func TestNarrowToChange_FreesAWaitingTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	mine := f.ticket(t, "Wide", "server/cmd/api/")
	wants := f.ticket(t, "Wants main", "server/cmd/api/main.go")
	f.claimOnly(t, a, mine.ID)
	require.Nil(t, f.claimOnly(t, b, wants.ID).Ticket)

	result, err := f.svc.NarrowToChange(f.ctx, a, mine.ID, []string{"server/cmd/api/modules.go"}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"server/cmd/api/modules.go"}, f.paths(t, mine.ID))

	granted := f.claimOnly(t, b, wants.ID)
	require.NotNil(t, granted.Ticket, "the waiting ticket is claimable")
}

func TestNarrowToChange_NothingToNarrowChangesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	mine := f.ticket(t, "Narrow", "server/cmd/api/modules.go")
	f.claimOnly(t, a, mine.ID)

	result, err := f.svc.NarrowToChange(f.ctx, a, mine.ID, []string{"server/cmd/api/modules.go"}, nil)
	require.NoError(t, err)
	assert.Nil(t, result)
	assert.Equal(t, []string{"server/cmd/api/modules.go"}, f.paths(t, mine.ID))
}

func TestNarrowToChange_ALeaseOnAnotherTicketIsLeftAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	old := f.ticket(t, "Old", "server/old/")
	mine := f.ticket(t, "Now", "server/cmd/api/")
	f.claimOnly(t, a, mine.ID)

	result, err := f.svc.NarrowToChange(f.ctx, a, old.ID, []string{"server/cmd/api/modules.go"}, nil)

	require.NoError(t, err)
	assert.Nil(t, result)
	assert.Equal(t, []string{"server/cmd/api/"}, f.paths(t, mine.ID))
}
