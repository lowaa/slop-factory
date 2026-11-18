package ticket_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// newService wires the real Service onto the real PostgresStore against a
// freshly-cloned database. The pair is tested together deliberately -- the
// rules live in Service and the SQL in the store, and it is the seam
// between them (a normalized path actually reaching a row, a status
// actually setting closed_at) that these tests are for.
//
// ctx is scoped to a made-up tenant rather than a real global_tenants row:
// nothing here creates an agent (that needs a real row -- see
// lease_test.go's fixture), so RLS is all a bare uuid needs to satisfy.
func newService(t *testing.T) (*ticket.Service, context.Context) {
	t.Helper()
	database := dbtest.WithDB(t)
	store := newStore(database)
	ctx := db.WithTenant(context.Background(), uuid.New())
	return ticket.NewService(store, slog.DiscardHandler), ctx
}

func TestCreateAndGet_RoundTripsPathsAndDefaults(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)

	created, err := svc.Create(ctx, &ticket.CreateParams{
		Title: "Add the lease table",
		Body:  "See the design doc",
		Paths: []model.Path{
			{Path: "./server/internal/lease/"},
			{Path: "server/internal/schema/migrations/000002_leases.sql"},
		},
	})
	require.NoError(t, err)
	assert.NotZero(t, created.ID)
	assert.Equal(t, model.PriorityNormal, created.Priority, "priority should default")
	assert.Equal(t, ticket.StatusOpen, created.Status)
	assert.Nil(t, created.CreatedByAgentID, "a ticket the human filed has no agent author")

	got, err := svc.Get(ctx, created.ID)
	require.NoError(t, err)

	// "./server/internal/lease/" must have been normalized on the way in,
	// or the conflict rule would compare scopes in two different forms.
	assert.Equal(t, []model.Path{
		{Path: "server/internal/lease/", Source: model.PathSourceFiled},
		{Path: "server/internal/schema/migrations/000002_leases.sql", Source: model.PathSourceFiled},
	}, got.Paths)
}

func TestCreate_RejectsSelfOverlappingScopes(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)

	_, err := svc.Create(ctx, &ticket.CreateParams{
		Title: "Overlapping",
		Paths: []model.Path{{Path: "server/"}, {Path: "server/main.go"}},
	})
	require.ErrorIs(t, err, scope.ErrInvalidPath)
}

func TestCreate_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)

	_, err := svc.Create(ctx, &ticket.CreateParams{Title: ""})
	require.ErrorIs(t, err, ticket.ErrEmptyTitle)
}

func TestGet_MissingTicket(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)

	_, err := svc.Get(ctx, 9999)
	require.ErrorIs(t, err, ticket.ErrNotFound)
}

func TestList_FiltersByStatusAndAttachesPaths(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)

	open, err := svc.Create(ctx, &ticket.CreateParams{
		Title: "Still open",
		Paths: []model.Path{{Path: "frontend/"}},
	})
	require.NoError(t, err)

	done, err := svc.Create(ctx, &ticket.CreateParams{Title: "Finished"})
	require.NoError(t, err)
	doneStatus := ticket.StatusDone
	_, err = svc.Update(ctx, done.ID, &ticket.UpdateParams{Status: &doneStatus})
	require.NoError(t, err)

	all, err := svc.List(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	onlyOpen, err := svc.List(ctx, &ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusOpen}})
	require.NoError(t, err)
	require.Len(t, onlyOpen, 1)
	assert.Equal(t, open.ID, onlyOpen[0].ID)
	// The board renders scopes on every card, so the list path must attach
	// them rather than leaving the caller to fetch each ticket.
	assert.Equal(t, []model.Path{{Path: "frontend/", Source: model.PathSourceFiled}}, onlyOpen[0].Paths)
}

func TestUpdate_StatusDrivesClosedAt(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)

	created, err := svc.Create(ctx, &ticket.CreateParams{Title: "Closes and reopens"})
	require.NoError(t, err)
	assert.Nil(t, created.ClosedAt)

	done := ticket.StatusDone
	closed, err := svc.Update(ctx, created.ID, &ticket.UpdateParams{Status: &done})
	require.NoError(t, err)
	require.NotNil(t, closed.ClosedAt, "a terminal status should stamp closed_at")

	// Reopening must clear it, or the board shows a ticket that is both
	// open and closed.
	open := ticket.StatusOpen
	reopened, err := svc.Update(ctx, created.ID, &ticket.UpdateParams{Status: &open})
	require.NoError(t, err)
	assert.Nil(t, reopened.ClosedAt)
}

// A status change and the other fields of one edit are made together; a
// refused status writes none of them.
//
// Verified: with the fields dropped from an edit that also moves the
// status, the moved ticket keeps its old priority and paths; with every
// field but the priority written ahead of a refused move, the refusal's
// checks on them fail.
func TestUpdate_AStatusChangeWritesTheOtherFieldsWithIt(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)
	created, err := svc.Create(ctx, &ticket.CreateParams{Title: "Moved and edited", Paths: []model.Path{{Path: "server/"}}})
	require.NoError(t, err)

	cancelled, urgent, flagged := ticket.StatusCancelled, model.PriorityUrgent, true
	updated, err := svc.Update(ctx, created.ID, &ticket.UpdateParams{
		Status: &cancelled, Priority: &urgent, NeedsDiscussion: &flagged, RequiresApproval: &flagged,
		Paths: []model.Path{{Path: "frontend/"}},
	})
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusCancelled, updated.Status)
	assert.Equal(t, model.PriorityUrgent, updated.Priority)
	assert.True(t, updated.NeedsDiscussion)
	assert.True(t, updated.RequiresApproval)
	require.Len(t, updated.Paths, 1)
	assert.Equal(t, "frontend/", updated.Paths[0].Path)

	parked, normal, unflagged := ticket.StatusParked, model.PriorityNormal, false
	_, err = svc.Update(ctx, created.ID, &ticket.UpdateParams{
		Status: &parked, Priority: &normal, NeedsDiscussion: &unflagged, RequiresApproval: &unflagged,
		Paths: []model.Path{{Path: "web/"}},
	})
	require.ErrorIs(t, err, ticket.ErrNotParkable)
	got, err := svc.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PriorityUrgent, got.Priority, "a refused move writes nothing")
	assert.True(t, got.NeedsDiscussion, "a refused move writes nothing")
	assert.True(t, got.RequiresApproval, "a refused move writes nothing")
	require.Len(t, got.Paths, 1)
	assert.Equal(t, "frontend/", got.Paths[0].Path, "a refused move writes nothing")
}

// A single edit's refusal reads as the single routes always have, not as a
// batch of one.
//
// Verified: with Update returning the batch's error, the message is the
// batch's.
func TestUpdate_ARefusalNamesTheTicketAndTheMove(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)
	created, err := svc.Create(ctx, &ticket.CreateParams{Title: "Drafting", Status: ticket.StatusPreparing})
	require.NoError(t, err)

	open := ticket.StatusOpen
	_, err = svc.Update(ctx, created.ID, &ticket.UpdateParams{Status: &open})
	require.ErrorIs(t, err, ticket.ErrLeavePreparing)
	assert.Equal(t, fmt.Sprintf("%s: #%d to open", ticket.ErrLeavePreparing, created.ID), err.Error())

	_, err = svc.MarkReady(ctx, created.ID)
	require.NoError(t, err)
	_, err = svc.MarkReady(ctx, created.ID)
	require.ErrorIs(t, err, ticket.ErrNotPreparing)
	assert.Equal(t, fmt.Sprintf("%s: #%d", ticket.ErrNotPreparing, created.ID), err.Error())
}

func TestUpdate_ReplacesPathScopes(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)

	created, err := svc.Create(ctx, &ticket.CreateParams{
		Title: "Rescoped",
		Paths: []model.Path{{Path: "server/"}},
	})
	require.NoError(t, err)

	updated, err := svc.Update(ctx, created.ID, &ticket.UpdateParams{
		Paths: []model.Path{{Path: "frontend/src/"}},
	})
	require.NoError(t, err)
	assert.Equal(t, []model.Path{{Path: "frontend/src/", Source: model.PathSourceFiled}}, updated.Paths)
}

func TestUpdate_MissingTicket(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)

	priority := model.PriorityHigh
	_, err := svc.Update(ctx, 9999, &ticket.UpdateParams{Priority: &priority})
	require.ErrorIs(t, err, ticket.ErrNotFound)
}

func TestComments_RoundTripAndMissingTicket(t *testing.T) {
	t.Parallel()
	database := dbtest.WithDB(t)
	svc := ticket.NewService(newStore(database), slog.DiscardHandler)
	ctx := db.WithTenant(context.Background(), uuid.New())
	// author_user_id carries a real foreign key onto global_users, unlike
	// tenant_id -- see dbtest.NewUser's doc comment.
	human := dbtest.NewUser(t, database)

	created, err := svc.Create(ctx, &ticket.CreateParams{Title: "Has comments"})
	require.NoError(t, err)

	_, err = svc.AddComment(ctx, created.ID, ticket.UserAuthor(human), "decided to split this")
	require.NoError(t, err)

	comments, err := svc.ListComments(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "decided to split this", comments[0].Body)
	assert.Nil(t, comments[0].AuthorAgentID, "the human's own comments have no agent author")
	require.NotNil(t, comments[0].AuthorUserID)
	assert.Equal(t, human, *comments[0].AuthorUserID)

	_, err = svc.AddComment(ctx, 9999, ticket.UserAuthor(human), "orphan")
	require.ErrorIs(t, err, ticket.ErrNotFound,
		"a comment on a missing ticket should be a 404, not a constraint violation")

	_, err = svc.AddComment(ctx, created.ID, ticket.UserAuthor(human), "")
	require.ErrorIs(t, err, ticket.ErrEmptyComment)

	_, err = svc.AddComment(ctx, created.ID, ticket.Author{}, "no author at all")
	require.ErrorIs(t, err, ticket.ErrInvalidAuthor, "a comment must have exactly one author")

	agentID := uuid.New()
	both := ticket.Author{AgentID: &agentID, UserID: &human}
	_, err = svc.AddComment(ctx, created.ID, both, "both an agent and a user")
	require.ErrorIs(t, err, ticket.ErrInvalidAuthor,
		"exactly one author, not at most one -- both set is as invalid as neither")
}

// A ticket may be created by an agent, by a user, or by neither (seeded
// data) -- but never claimed by both at once, which the schema's
// num_nonnulls(...) <= 1 check backs and this pins at the service layer so
// the caller gets a 422 rather than a raw constraint violation.
func TestCreate_RejectsBothAnAgentAndAUserCreator(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)
	agentID := uuid.New()
	userID := uuid.New()

	_, err := svc.Create(ctx, &ticket.CreateParams{
		Title:            "Ambiguous creator",
		CreatedByAgentID: &agentID,
		CreatedByUserID:  &userID,
	})
	require.ErrorIs(t, err, ticket.ErrMultipleCreators)
}

// Invalid enum values must be caught in the service, not left to the
// database's CHECK constraint. A constraint violation arrives as an opaque
// driver error that mapError can't distinguish from a genuine internal
// fault, so the client would get a 500 for what is plainly bad input.
func TestEnumValidation_IsCaughtBeforeTheDatabase(t *testing.T) {
	t.Parallel()
	svc, ctx := newService(t)

	created, err := svc.Create(ctx, &ticket.CreateParams{Title: "valid"})
	require.NoError(t, err)

	t.Run("create with a bogus priority", func(t *testing.T) {
		_, err := svc.Create(ctx, &ticket.CreateParams{Title: "x", Priority: "bogus"})
		require.ErrorIs(t, err, ticket.ErrInvalidEnum)
	})

	t.Run("update to a bogus status", func(t *testing.T) {
		bogus := ticket.Status("bogus")
		_, err := svc.Update(ctx, created.ID, &ticket.UpdateParams{Status: &bogus})
		require.ErrorIs(t, err, ticket.ErrInvalidEnum)
	})

	t.Run("update to a bogus priority", func(t *testing.T) {
		bogus := model.Priority("bogus")
		_, err := svc.Update(ctx, created.ID, &ticket.UpdateParams{Priority: &bogus})
		require.ErrorIs(t, err, ticket.ErrInvalidEnum)
	})

	t.Run("filter by a bogus status", func(t *testing.T) {
		_, err := svc.List(ctx, &ticket.ListFilter{Statuses: []ticket.Status{"bogus"}})
		require.ErrorIs(t, err, ticket.ErrInvalidEnum)
	})

	t.Run("bogus path source", func(t *testing.T) {
		_, err := svc.Create(ctx, &ticket.CreateParams{
			Title: "x",
			Paths: []model.Path{{Path: "server/", Source: "bogus"}},
		})
		require.ErrorIs(t, err, ticket.ErrInvalidEnum)
	})

	t.Run("valid values still pass", func(t *testing.T) {
		urgent := model.PriorityUrgent
		updated, err := svc.Update(ctx, created.ID, &ticket.UpdateParams{Priority: &urgent})
		require.NoError(t, err)
		assert.Equal(t, model.PriorityUrgent, updated.Priority)
	})
}
