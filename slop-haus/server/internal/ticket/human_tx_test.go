package ticket_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// What a ticket looks like after a human's move, with everything that
// differs between two tickets for no reason (ids, times) left out.
type outcome struct {
	Status          ticket.Status
	NeedsDiscussion bool
	HasResumeAgent  bool
	HasAffinity     bool
	Approved        bool
	ChangesAsked    bool
	Closed          bool
	Comments        []string
	History         []change
	Err             string
}

func (f *fixture) outcomeOf(t *testing.T, id int64, err error) outcome {
	t.Helper()
	if err != nil {
		return outcome{Err: strings.ReplaceAll(err.Error(), fmt.Sprintf("#%d", id), "#N")}
	}
	got, getErr := f.svc.Get(f.ctx, id)
	require.NoError(t, getErr)
	o := outcome{
		Status: got.Status, NeedsDiscussion: got.NeedsDiscussion, HasResumeAgent: got.ResumeAgentID != nil,
		HasAffinity: got.AffinityUntil != nil, Approved: got.ApprovedAt != nil,
		ChangesAsked: got.ChangesRequestedAt != nil, Closed: got.ClosedAt != nil,
	}
	for _, c := range f.comments(t, id) {
		who := "agent"
		if c.AuthorUserID != nil {
			who = "user"
		}
		o.Comments = append(o.Comments, who+": "+c.Body)
	}
	for _, h := range f.history(t, id) {
		h.Ticket = 0
		if h.Actor == "agent" {
			h.Who = nil // two agents, one per run
		}
		o.History = append(o.History, h)
	}
	return o
}

// inTx runs fn through the exported, composable moves the way an approval
// will: in a transaction of the caller's.
func (f *fixture) inTx(t *testing.T, fn func(ctx context.Context, tx ticket.TransitionTx) error) error {
	t.Helper()
	return f.svc.InTransition(f.ctx, func(tx ticket.TransitionTx) error { return fn(f.ctx, tx) })
}

func (f *fixture) setStatusCtx(ctx context.Context, id int64, status ticket.Status) (*ticket.Ticket, error) {
	return f.svc.Update(ctx, id, &ticket.UpdateParams{Status: &status})
}

// A move made through the Tx-composable function and the same move through
// the route a human clicks must leave identical tickets, so an approved
// proposal cannot be allowed something the human's own click is refused, or
// the reverse.
func TestHumanMovesInTx_MatchTheServiceRoutes(t *testing.T) {
	t.Parallel()
	type move struct {
		name string
		// want is the status a successful move lands on: the routes and the
		// Tx variants now share their code, so comparing them proves they
		// stay shared, and this pins what that code does.
		want ticket.Status
		// setup returns a ticket in the state the move starts from.
		setup  func(f *fixture, t *testing.T, agent uuid.UUID) int64
		direct func(f *fixture, ctx context.Context, id int64) error
		inTx   func(f *fixture, ctx context.Context, tx ticket.TransitionTx, id int64) error
	}
	discussing := func(f *fixture, t *testing.T, a uuid.UUID) int64 {
		created := f.ticket(t, "Loose")
		f.claim(t, a)
		f.discuss(t, a)
		return created.ID
	}
	inReview := func(f *fixture, t *testing.T, a uuid.UUID) int64 {
		return f.inReviewWith(t, a, "Reviewed", "https://github.com/o/r/pull/1").ID
	}
	moves := []move{
		{"discussion answer", ticket.StatusOpen, discussing,
			func(f *fixture, ctx context.Context, id int64) error {
				_, err := f.svc.AddComment(ctx, id, ticket.UserAuthor(f.human), "Flat shape.")
				return err
			},
			func(f *fixture, ctx context.Context, tx ticket.TransitionTx, id int64) error {
				_, err := ticket.CommentAsUser(ctx, tx, &ticket.HumanComment{
					TicketID: id, UserID: f.human, Body: "Flat shape.", Affinity: ticket.DefaultResumeAffinity})
				return err
			}},
		{"ready", ticket.StatusOpen, func(f *fixture, t *testing.T, _ uuid.UUID) int64 { return f.preparing(t, "Shaping").ID },
			func(f *fixture, ctx context.Context, id int64) error { _, err := f.svc.MarkReady(ctx, id); return err },
			func(f *fixture, ctx context.Context, tx ticket.TransitionTx, id int64) error {
				_, err := ticket.MarkTicketReady(ctx, tx, id)
				return err
			}},
		{"ready on an open ticket is refused", "", func(f *fixture, t *testing.T, _ uuid.UUID) int64 { return f.ticket(t, "Queued").ID },
			func(f *fixture, ctx context.Context, id int64) error { _, err := f.svc.MarkReady(ctx, id); return err },
			func(f *fixture, ctx context.Context, tx ticket.TransitionTx, id int64) error {
				_, err := ticket.MarkTicketReady(ctx, tx, id)
				return err
			}},
		{"park", ticket.StatusParked, func(f *fixture, t *testing.T, _ uuid.UUID) int64 { return f.ticket(t, "Someday").ID },
			func(f *fixture, ctx context.Context, id int64) error {
				_, err := f.setStatusCtx(ctx, id, ticket.StatusParked)
				return err
			},
			func(f *fixture, ctx context.Context, tx ticket.TransitionTx, id int64) error {
				_, err := ticket.MoveTicketTo(ctx, tx, id, ticket.StatusParked)
				return err
			}},
		{"cancel", ticket.StatusCancelled, func(f *fixture, t *testing.T, _ uuid.UUID) int64 { return f.ticket(t, "Never").ID },
			func(f *fixture, ctx context.Context, id int64) error {
				_, err := f.setStatusCtx(ctx, id, ticket.StatusCancelled)
				return err
			},
			func(f *fixture, ctx context.Context, tx ticket.TransitionTx, id int64) error {
				_, err := ticket.MoveTicketTo(ctx, tx, id, ticket.StatusCancelled)
				return err
			}},
		{"park a cancelled ticket is refused", "", func(f *fixture, t *testing.T, _ uuid.UUID) int64 {
			id := f.ticket(t, "Gone").ID
			_, err := f.setStatus(t, id, ticket.StatusCancelled)
			require.NoError(t, err)
			return id
		},
			func(f *fixture, ctx context.Context, id int64) error {
				_, err := f.setStatusCtx(ctx, id, ticket.StatusParked)
				return err
			},
			func(f *fixture, ctx context.Context, tx ticket.TransitionTx, id int64) error {
				_, err := ticket.MoveTicketTo(ctx, tx, id, ticket.StatusParked)
				return err
			}},
		{"approve", ticket.StatusOpen, inReview,
			func(f *fixture, ctx context.Context, id int64) error {
				_, err := f.svc.Approve(ctx, id, f.human)
				return err
			},
			func(f *fixture, ctx context.Context, tx ticket.TransitionTx, id int64) error {
				_, err := ticket.ApproveReview(ctx, tx, id, nil, f.human, ticket.DefaultResumeAffinity)
				return err
			}},
		{"approve outside review is refused", "", func(f *fixture, t *testing.T, _ uuid.UUID) int64 { return f.ticket(t, "Queued").ID },
			func(f *fixture, ctx context.Context, id int64) error {
				_, err := f.svc.Approve(ctx, id, f.human)
				return err
			},
			func(f *fixture, ctx context.Context, tx ticket.TransitionTx, id int64) error {
				_, err := ticket.ApproveReview(ctx, tx, id, nil, f.human, ticket.DefaultResumeAffinity)
				return err
			}},
		{"request changes", ticket.StatusOpen, inReview,
			func(f *fixture, ctx context.Context, id int64) error {
				_, err := f.svc.RequestChanges(ctx, id, f.human, "Rename it.")
				return err
			},
			func(f *fixture, ctx context.Context, tx ticket.TransitionTx, id int64) error {
				_, err := ticket.RequestReviewChanges(ctx, tx, id, nil, f.human, "Rename it.", ticket.DefaultResumeAffinity)
				return err
			}},
		{"request changes without a comment is refused", "", inReview,
			func(f *fixture, ctx context.Context, id int64) error {
				_, err := f.svc.RequestChanges(ctx, id, f.human, " ")
				return err
			},
			func(f *fixture, ctx context.Context, tx ticket.TransitionTx, id int64) error {
				_, err := ticket.RequestReviewChanges(ctx, tx, id, nil, f.human, " ", ticket.DefaultResumeAffinity)
				return err
			}},
	}

	for _, m := range moves {
		t.Run(m.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := ticket.WithUser(f.ctx, f.human)

			directID := m.setup(f, t, f.agent(t, "builder-1"))
			directErr := m.direct(f, ctx, directID)
			direct := f.outcomeOf(t, directID, directErr)

			// A second ticket from the same start, so the two share no state.
			txID := m.setup(f, t, f.agent(t, "builder-2"))
			txErr := f.inTx(t, func(ctx context.Context, tx ticket.TransitionTx) error {
				return m.inTx(f, ticket.WithUser(ctx, f.human), tx, txID)
			})
			viaTx := f.outcomeOf(t, txID, txErr)

			assert.Equal(t, direct, viaTx)
			assert.Equal(t, m.want, direct.Status)
			if direct.Err == "" {
				assert.NotEmpty(t, direct.History, "the move left a record, so there was something to compare")
			}
		})
	}
}

// A move made in the caller's transaction and a write the caller makes in it
// stand or fall together.
func TestHumanMovesInTx_ShareTheCallersTransaction(t *testing.T) {
	t.Parallel()
	titleOf := func(f *fixture, t *testing.T, id int64) string {
		got, err := f.svc.Get(f.ctx, id)
		require.NoError(t, err)
		return got.Title
	}
	run := func(f *fixture, t *testing.T, id int64, fail error) error {
		return f.inTx(t, func(ctx context.Context, tx ticket.TransitionTx) error {
			if _, err := ticket.MarkTicketReady(ticket.WithUser(ctx, f.human), tx, id); err != nil {
				return err
			}
			if _, err := tx.Querier().Exec(ctx, `UPDATE tickets SET title = 'written by the caller' WHERE id = $1`, id); err != nil {
				return err
			}
			return fail
		})
	}

	t.Run("rolled back together", func(t *testing.T) {
		f := newFixture(t)
		created := f.preparing(t, "Shaping")

		require.ErrorIs(t, run(f, t, created.ID, assert.AnError), assert.AnError)

		got, err := f.svc.Get(f.ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, ticket.StatusPreparing, got.Status)
		assert.Equal(t, "Shaping", got.Title)
		assert.Len(t, f.history(t, created.ID), 1, "only the creation is recorded")
	})

	t.Run("committed together", func(t *testing.T) {
		f := newFixture(t)
		created := f.preparing(t, "Shaping")

		require.NoError(t, run(f, t, created.ID, nil))

		got, err := f.svc.Get(f.ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, ticket.StatusOpen, got.Status)
		assert.Equal(t, "written by the caller", titleOf(f, t, created.ID))
	})
}

func TestHumanMovesInTx_RefuseWhatTheRoutesRefuseBeforeWriting(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Queued")

	err := f.inTx(t, func(ctx context.Context, tx ticket.TransitionTx) error {
		_, err := ticket.MoveTicketTo(ctx, tx, created.ID, ticket.Status("nonsense"))
		return err
	})
	require.ErrorIs(t, err, ticket.ErrInvalidEnum)

	err = f.inTx(t, func(ctx context.Context, tx ticket.TransitionTx) error {
		_, err := ticket.CommentAsUser(ctx, tx, &ticket.HumanComment{TicketID: created.ID, UserID: f.human})
		return err
	})
	require.ErrorIs(t, err, ticket.ErrEmptyComment)
	assert.Empty(t, f.comments(t, created.ID))
}
