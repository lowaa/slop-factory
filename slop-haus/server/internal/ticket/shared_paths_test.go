package ticket_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

var migrations = manifest.SharedPathRule{Path: "db/migrations/", Resolve: scope.ResolveMigration}

// sharedFixture is a fixture whose tenant has a manifest with a root repo
// sharing db/migrations/ and a second repo, infra, sharing nothing.
func sharedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	store := manifest.NewPostgresStore(f.database)
	_, err := store.Put(f.ctx, []manifest.Repo{
		{Path: "mono", Remote: "github.com/acme/mono", SharedPaths: []manifest.SharedPathRule{
			migrations, {Path: "gen/*.ts", Resolve: scope.ResolveRegenerate},
			{Path: "docs/", Resolve: scope.ResolveMerge},
		}},
		{Path: "infra", Remote: "github.com/acme/infra"},
	}, f.human)
	require.NoError(t, err)
	f.svc = ticket.NewService(newStore(f.database, ticket.WithStoreWorkspace(manifest.NewPolicy(store))), slog.DiscardHandler,
		ticket.WithCodeHosts(codehosttest.Registry(codehosttest.NewLinkHost(prHost))),
		ticket.WithWorkspace(manifest.NewPolicy(store)))
	return f
}

func TestSharedPaths_TwoTicketsOverlappingOnlyInASharedPathAreBothClaimable(t *testing.T) {
	t.Parallel()
	f := sharedFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	first := f.ticket(t, "First", "mono/db/migrations/")
	second := f.ticket(t, "Second", "mono/db/")

	got, err := f.svc.Claim(f.ctx, a1, &first.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Ticket)
	assert.Empty(t, got.SharedOverlaps)

	got, err = f.svc.Claim(f.ctx, a2, &second.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Ticket, "mono/db/ overlaps db/migrations/ only inside the shared directory")
	assert.Nil(t, got.Blocked)
	assert.Equal(t, []ticket.SharedOverlap{{TicketID: first.ID, Path: "mono/db/migrations/", Resolve: scope.ResolveMigration}}, got.SharedOverlaps)
}

func TestSharedPaths_AMergePathDoesNotBlockAClaim(t *testing.T) {
	t.Parallel()
	f := sharedFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	first := f.ticket(t, "First", "mono/docs/design.md")
	second := f.ticket(t, "Second", "mono/docs/")

	_, err := f.svc.Claim(f.ctx, a1, &first.ID)
	require.NoError(t, err)
	got, err := f.svc.Claim(f.ctx, a2, &second.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Ticket, "mono/docs/ is a merge path, so the overlap blocks nothing")
	assert.Nil(t, got.Blocked)
	assert.Equal(t, []ticket.SharedOverlap{{TicketID: first.ID, Path: "mono/docs/design.md", Resolve: scope.ResolveMerge}}, got.SharedOverlaps)
	assert.Contains(t, ticket.SharedOverlapNotice(got.SharedOverlaps), "ticket_discuss")
}

func TestSharedPaths_AnOverlapOutsideTheSharedPathStillBlocks(t *testing.T) {
	t.Parallel()
	f := sharedFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	first := f.ticket(t, "First", "mono/db/migrations/", "mono/svc/x.go")
	second := f.ticket(t, "Second", "mono/db/migrations/", "mono/svc/")
	third := f.ticket(t, "Third", "mono/db/")

	_, err := f.svc.Claim(f.ctx, a1, &first.ID)
	require.NoError(t, err)

	got, err := f.svc.Claim(f.ctx, a2, &second.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Ticket, "its svc/ scope overlaps svc/x.go for real")
	require.NotNil(t, got.Blocked)
	assert.Equal(t, "mono/svc/x.go", got.Blocked.ConflictingPath)

	// db/ as a whole is wider than the shared directory, but what it shares
	// with the holder is only inside it.
	got, err = f.svc.Claim(f.ctx, a2, &third.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.Ticket)
}

func TestSharedPaths_AScopeWiderThanTheSharedPathAtBothEndsStillBlocks(t *testing.T) {
	t.Parallel()
	f := sharedFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	first := f.ticket(t, "First", "mono/db/")
	second := f.ticket(t, "Second", "mono/db/")

	_, err := f.svc.Claim(f.ctx, a1, &first.ID)
	require.NoError(t, err)
	got, err := f.svc.Claim(f.ctx, a2, &second.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Ticket, "they share all of db/, not only db/migrations/")
	assert.NotNil(t, got.Blocked)
}

func TestSharedPaths_APatternOfOneRepoDoesNotApplyToAnother(t *testing.T) {
	t.Parallel()
	f := sharedFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	first := f.ticket(t, "First", "infra/db/migrations/")
	second := f.ticket(t, "Second", "infra/db/migrations/")

	_, err := f.svc.Claim(f.ctx, a1, &first.ID)
	require.NoError(t, err)
	got, err := f.svc.Claim(f.ctx, a2, &second.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Ticket, "infra shares nothing; the root repo's rule is the root repo's")
	assert.NotNil(t, got.Blocked)
}

func TestSharedPaths_ExtendingIntoASharedPathIsGrantedAndReported(t *testing.T) {
	t.Parallel()
	f := sharedFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	first := f.ticket(t, "First", "mono/db/migrations/", "mono/svc/x.go")
	second := f.ticket(t, "Second", "mono/app/")
	_, err := f.svc.Claim(f.ctx, a1, &first.ID)
	require.NoError(t, err)
	_, err = f.svc.Claim(f.ctx, a2, &second.ID)
	require.NoError(t, err)

	denied, err := f.svc.ExtendScope(f.ctx, a2, []model.Path{{Path: "mono/svc/"}})
	require.NoError(t, err)
	assert.False(t, denied.Granted)

	got, err := f.svc.ExtendScope(f.ctx, a2, []model.Path{{Path: "mono/db/migrations/0002_x.sql"}, {Path: "mono/gen/api.ts"}})
	require.NoError(t, err)
	assert.True(t, got.Granted)
	assert.Equal(t, []ticket.SharedOverlap{{TicketID: first.ID, Path: "mono/db/migrations/0002_x.sql", Resolve: scope.ResolveMigration}}, got.SharedOverlaps)
}

func TestSharedPaths_AnOverlapInASharedPathIsStillReportedAndLabelled(t *testing.T) {
	t.Parallel()
	f := sharedFixture(t)
	mine := f.ticket(t, "Mine", "mono/db/migrations/", "mono/gen/api.ts")
	shared := f.ticket(t, "Shared", "mono/db/")
	real := f.ticket(t, "Real", "mono/gen/")

	got := f.overlaps(t, mine.ID)
	require.Len(t, got, 2)
	assert.Equal(t, ticket.ScopeOverlap{TicketID: shared.ID, Title: "Shared", Status: ticket.StatusOpen,
		Path: "mono/db/migrations/", Other: "mono/db/", Resolve: scope.ResolveMigration}, got[0])
	assert.Equal(t, ticket.ScopeOverlap{TicketID: real.ID, Title: "Real", Status: ticket.StatusOpen,
		Path: "mono/gen/api.ts", Other: "mono/gen/", Resolve: scope.ResolveRegenerate}, got[1])
}

func TestSharedPaths_WithoutAManifestEveryOverlapBlocks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	first := f.ticket(t, "First", "mono/db/migrations/")
	second := f.ticket(t, "Second", "mono/db/migrations/")
	_, err := f.svc.Claim(f.ctx, a1, &first.ID)
	require.NoError(t, err)
	got, err := f.svc.Claim(f.ctx, a2, &second.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Ticket)
	assert.NotNil(t, got.Blocked)
}

func TestSharedOverlapNotice_SaysWhatToDoPerKind(t *testing.T) {
	t.Parallel()
	assert.Empty(t, ticket.SharedOverlapNotice(nil))

	got := ticket.SharedOverlapNotice([]ticket.SharedOverlap{
		{TicketID: 7, Path: "mono/db/migrations/", Resolve: scope.ResolveMigration},
		{TicketID: 8, Path: "mono/gen/a.ts", Resolve: scope.ResolveRegenerate},
	})

	assert.Contains(t, got, "mono/db/migrations/, also held by #7 (migration)")
	assert.Contains(t, got, "update LATEST")
	assert.Contains(t, got, "mono/gen/a.ts, also held by #8 (regenerate)")
	assert.Contains(t, got, "never hand-merge")
}

func TestSharedPaths_PeekAgreesWithClaim(t *testing.T) {
	t.Parallel()
	f := sharedFixture(t)
	f.ticket(t, "Holder", "mono/db/migrations/")
	f.claim(t, f.agent(t, "holder"))
	f.ticket(t, "Beside", "mono/db/")

	got := f.peek(t, f.agent(t, "idle"))

	require.NotNil(t, got.Ticket, "the shared-only overlap does not hide it from a probe")
	assert.Nil(t, got.Blocked)
}

func TestSharedPaths_AHumanMovesATicketIntoReviewOverASharedScope(t *testing.T) {
	t.Parallel()
	f := sharedFixture(t)
	f.holding(t, "holder", false, "mono/db/migrations/", "mono/svc/x.go")
	shared := f.gated(t, "Shared only", "mono/db/")
	real := f.gated(t, "Real", "mono/svc/")

	got, err := f.setStatus(t, shared.ID, ticket.StatusReview)
	require.NoError(t, err, "mono/db/ overlaps the holder only inside db/migrations/")
	assert.Equal(t, ticket.StatusReview, got.Status)

	_, err = f.setStatus(t, real.ID, ticket.StatusReview)
	assert.ErrorIs(t, err, ticket.ErrScopeConflict, "mono/svc/ overlaps svc/x.go for real")
}

func TestSharedPaths_HeldScopeOverlapsAreLabelled(t *testing.T) {
	t.Parallel()
	f := sharedFixture(t)
	held := f.ticket(t, "Held", "mono/db/migrations/", "mono/svc/")
	f.claim(t, f.agent(t, "worker"))

	got := f.heldOverlaps(t, "mono/db/migrations/0002_x.sql", "mono/svc/a.go")

	assert.Equal(t, []ticket.HeldScopeOverlap{
		{TicketID: held.ID, Title: "Held", Status: ticket.StatusInProgress, Path: "mono/db/migrations/0002_x.sql", Held: "mono/db/migrations/", Resolve: scope.ResolveMigration},
		{TicketID: held.ID, Title: "Held", Status: ticket.StatusInProgress, Path: "mono/svc/a.go", Held: "mono/svc/"},
	}, got)
}

func TestSharedPaths_TheTicketBodyCarriesTheResolveKind(t *testing.T) {
	t.Parallel()
	f := sharedFixture(t)
	live := f.ticket(t, "Live", "mono/db/migrations/")
	mine := f.ticket(t, "Mine", "mono/db/")
	f.forceStatus(t, live.ID, ticket.StatusInProgress)

	rec := send(t, f.boardMux(), http.MethodGet, fmt.Sprintf("/api/tickets/%d", mine.ID), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []ticket.ScopeOverlapBody{{
		TicketID: live.ID, Title: "Live", Status: ticket.StatusInProgress, Path: "mono/db/", Other: "mono/db/migrations/", Resolve: scope.ResolveMigration,
	}}, decodeTicket(t, rec.Body.Bytes()).ScopeOverlaps)
}
