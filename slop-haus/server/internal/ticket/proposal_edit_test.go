package ticket_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func strp(s string) *string { return &s }

func TestEditTicketText_InTxRecordsTheRevisionAsThePerson(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Old title")

	err := f.inTx(t, func(ctx context.Context, tx ticket.TransitionTx) error {
		res, err := ticket.EditTicketText(ctx, tx, created.ID, f.human, &ticket.EditParams{Title: strp("New title"), BaseRevision: &created.Revision})
		require.NoError(t, err)
		assert.True(t, res.Changed)
		assert.Equal(t, "New title", res.Ticket.Title)
		return nil
	})
	require.NoError(t, err)

	revisions, err := f.svc.ListRevisions(f.ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, revisions, 1)
	require.NotNil(t, revisions[0].Editor.UserID)
	assert.Equal(t, f.human, *revisions[0].Editor.UserID)
	assert.Equal(t, "Old title", *revisions[0].OldTitle)
}

func TestEditTicketText_AStaleBaseRevisionRollsBackTheWholeTransaction(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Title")
	_, err := f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{Body: strp("someone else's")})
	require.NoError(t, err)

	err = f.inTx(t, func(ctx context.Context, tx ticket.TransitionTx) error {
		if _, err := tx.Comment(ctx, created.ID, &ticket.NewComment{By: ticket.UserAuthor(f.human), Body: "written beside the edit"}); err != nil {
			return err
		}
		_, err := ticket.EditTicketText(ctx, tx, created.ID, f.human, &ticket.EditParams{Body: strp("mine"), BaseRevision: &created.Revision})
		return err
	})
	require.ErrorIs(t, err, ticket.ErrStaleRevision)
	assert.Empty(t, f.comments(t, created.ID), "the write beside it rolled back")
	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "someone else's", got.Body)
}

func TestEditTicketText_RestatingTheTextRecordsNothingAndAnEmptyEditIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Title")

	err := f.inTx(t, func(ctx context.Context, tx ticket.TransitionTx) error {
		res, err := ticket.EditTicketText(ctx, tx, created.ID, f.human, &ticket.EditParams{Title: strp("Title")})
		require.NoError(t, err)
		assert.False(t, res.Changed)
		_, err = ticket.EditTicketText(ctx, tx, created.ID, f.human, &ticket.EditParams{})
		assert.ErrorIs(t, err, ticket.ErrEmptyEdit)
		return nil
	})
	require.NoError(t, err)
	revisions, err := f.svc.ListRevisions(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Empty(t, revisions)
}

func TestScopeOverlapsWith_JudgesTheGivenPathsNotTheStoredOnes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.ticket(t, "Mine", "mine/")
	other := f.ticket(t, "Other", "other/src/")

	assert.Empty(t, f.overlaps(t, mine.ID))

	got, err := f.svc.ScopeOverlapsWith(f.ctx, mine.ID, []model.Path{{Path: "other/"}})
	require.NoError(t, err)
	assert.Equal(t, []int64{other.ID}, overlapIDs(got))

	got, err = f.svc.ScopeOverlapsWith(f.ctx, mine.ID, []model.Path{{Path: "elsewhere/"}})
	require.NoError(t, err)
	assert.Empty(t, got)
}
