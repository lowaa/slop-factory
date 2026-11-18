package pendingaction_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (f *autoFixture) stewardApproved(t *testing.T, p *pendingaction.ProposeParams) *pendingaction.Action {
	t.Helper()
	p.AgentID = f.consoleFor(t, f.a)
	proposed, err := f.svc.Propose(f.ctx, p)
	require.NoError(t, err)
	got, err := f.svc.ApproveForSteward(f.ctx, proposed.ID)
	require.NoError(t, err)
	return got
}

func (f *autoFixture) commentsOn(t *testing.T, id int64) []string {
	t.Helper()
	rows, err := f.database.Query(f.ctx, `SELECT body FROM ticket_comments WHERE ticket_id = $1 ORDER BY id`, id)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var body string
		require.NoError(t, rows.Scan(&body))
		out = append(out, body)
	}
	return out
}

func TestRevertingAStewardReadyPutsTheTicketBackToPreparing(t *testing.T) {
	f := newAutoFixture(t)
	got := f.stewardApproved(t, &pendingaction.ProposeParams{TicketID: f.ticketIn(t, ticket.StatusPreparing), Kind: pendingaction.KindReady})
	require.Equal(t, ticket.StatusOpen, f.ticketStatus(t, *got.TicketID))

	require.NoError(t, f.svc.RevertStewardApproval(f.ctx, f.a, got.ID))

	assert.Equal(t, ticket.StatusPreparing, f.ticketStatus(t, *got.TicketID))
	assert.Contains(t, f.commentsOn(t, *got.TicketID)[0], "Taken back")
	var cause string
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT cause FROM ticket_status_changes WHERE ticket_id = $1 ORDER BY id DESC LIMIT 1`, got.TicketID).Scan(&cause))
	assert.Equal(t, pendingaction.StewardUndoCauseFor(got.ID), cause)
}

func TestRevertingAStewardAnswerPutsTheTicketBackInDiscussion(t *testing.T) {
	f := newAutoFixture(t)
	id := f.ticketIn(t, ticket.StatusDiscussion)
	got := f.stewardApproved(t, answer(uuid.Nil, id, "go with B"))
	require.Equal(t, ticket.StatusOpen, f.ticketStatus(t, id), "the answer re-queued it")

	require.NoError(t, f.svc.RevertStewardApproval(f.ctx, f.a, got.ID))

	assert.Equal(t, ticket.StatusDiscussion, f.ticketStatus(t, id))
	comments := f.commentsOn(t, id)
	assert.Contains(t, comments[len(comments)-1], "Taken back")
}

func TestRevertingAStewardEditRestoresWhatItReplaced(t *testing.T) {
	f := newAutoFixture(t)
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "old title", Body: "old body", Priority: model.PriorityNormal, Status: ticket.StatusPreparing,
		Paths: []model.Path{{Path: "keep.go", Source: model.PathSourceFiled}},
	})
	require.NoError(t, err)
	got := f.stewardApproved(t, &pendingaction.ProposeParams{TicketID: created.ID, Kind: pendingaction.KindEdit,
		Payload: pendingaction.Payload{Edit: &pendingaction.EditPayload{BaseRevision: created.Revision,
			Title: ptr("new title"), Paths: []string{"other.go"}, Priority: ptr(model.PriorityHigh), Open: true}}})
	edited, err := f.tickets.Get(f.ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "new title", edited.Title)
	require.Equal(t, ticket.StatusOpen, edited.Status)

	require.NoError(t, f.svc.RevertStewardApproval(f.ctx, f.a, got.ID))

	back, err := f.tickets.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "old title", back.Title)
	assert.Equal(t, "old body", back.Body)
	assert.Equal(t, model.PriorityNormal, back.Priority)
	assert.Equal(t, ticket.StatusPreparing, back.Status)
	require.Len(t, back.Paths, 1)
	assert.Equal(t, "keep.go", back.Paths[0].Path)
}

func TestARevertIsRefusedOnceTheTicketMovedOn(t *testing.T) {
	cases := map[string]func(f *autoFixture, id int64){
		"claimed by an agent": func(f *autoFixture, id int64) {
			f.exec(t, `UPDATE tickets SET status = 'in_progress' WHERE id = $1`, id)
		},
		"moved elsewhere": func(f *autoFixture, id int64) {
			f.exec(t, `UPDATE tickets SET status = 'parked' WHERE id = $1`, id)
		},
	}
	for name, move := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAutoFixture(t)
			got := f.stewardApproved(t, &pendingaction.ProposeParams{TicketID: f.ticketIn(t, ticket.StatusPreparing), Kind: pendingaction.KindReady})
			move(f, *got.TicketID)
			before := f.ticketStatus(t, *got.TicketID)

			err := f.svc.RevertStewardApproval(f.ctx, f.a, got.ID)

			assert.ErrorIs(t, err, pendingaction.ErrNotRevertable)
			assert.Equal(t, before, f.ticketStatus(t, *got.TicketID))
		})
	}
}

func TestAnEditIsNotRevertedOverALaterEdit(t *testing.T) {
	f := newAutoFixture(t)
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "old", Body: "b", Status: ticket.StatusPreparing})
	require.NoError(t, err)
	got := f.stewardApproved(t, &pendingaction.ProposeParams{TicketID: created.ID, Kind: pendingaction.KindEdit,
		Payload: pendingaction.Payload{Edit: &pendingaction.EditPayload{BaseRevision: created.Revision, Title: ptr("mine")}}})
	_, err = f.tickets.Edit(f.ctx, created.ID, f.b.ID, &ticket.EditParams{Title: ptr("someone else's")})
	require.NoError(t, err)

	err = f.svc.RevertStewardApproval(f.ctx, f.a, got.ID)

	assert.ErrorIs(t, err, pendingaction.ErrNotRevertable)
	back, err := f.tickets.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "someone else's", back.Title)
}

func TestOnlyWhatTheStewardAppliedAndHasAnInverseIsReverted(t *testing.T) {
	f := newAutoFixture(t)
	console := f.consoleFor(t, f.a)
	clicked := f.proposeReady(t, console)
	_, err := f.svc.Approve(f.ctx, f.a, clicked.ID)
	require.NoError(t, err)
	assert.ErrorIs(t, f.svc.RevertStewardApproval(f.ctx, f.a, clicked.ID), pendingaction.ErrNotRevertable, "a click is not the steward's")

	pending := f.proposeReady(t, console)
	assert.ErrorIs(t, f.svc.RevertStewardApproval(f.ctx, f.a, pending.ID), pendingaction.ErrNotRevertable, "nothing was applied")

	status := f.stewardApproved(t, &pendingaction.ProposeParams{TicketID: f.ticketIn(t, ticket.StatusOpen), Kind: pendingaction.KindStatus,
		Payload: pendingaction.Payload{Body: "not needed", Status: ticket.StatusParked}})
	assert.ErrorIs(t, f.svc.RevertStewardApproval(f.ctx, f.a, status.ID), pendingaction.ErrNotRevertable, "a status move has no inverse here")
	assert.Equal(t, ticket.StatusParked, f.ticketStatus(t, *status.TicketID))
}

func TestARevertIsRefusedWhenTheTicketMovedAwayAndBackToOpen(t *testing.T) {
	f := newAutoFixture(t)
	got := f.stewardApproved(t, &pendingaction.ProposeParams{TicketID: f.ticketIn(t, ticket.StatusPreparing), Kind: pendingaction.KindReady})
	_, err := f.database.Exec(f.ctx, `INSERT INTO ticket_status_changes (ticket_id, from_status, to_status, cause, priority, actor_kind) VALUES ($1, 'open', 'open', 'someone else', 'normal', 'system')`, got.TicketID)
	require.NoError(t, err)

	assert.ErrorIs(t, f.svc.RevertStewardApproval(f.ctx, f.a, got.ID), pendingaction.ErrNotRevertable)
	assert.Equal(t, ticket.StatusOpen, f.ticketStatus(t, *got.TicketID))
}

func TestAnEditThatChangedNothingIsNotRevertedOverTheOwnersLaterEdit(t *testing.T) {
	f := newAutoFixture(t)
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "same", Body: "b", Status: ticket.StatusPreparing})
	require.NoError(t, err)
	got := f.stewardApproved(t, &pendingaction.ProposeParams{TicketID: created.ID, Kind: pendingaction.KindEdit,
		Payload: pendingaction.Payload{Edit: &pendingaction.EditPayload{BaseRevision: created.Revision, Title: ptr("same")}}})
	_, err = f.tickets.Edit(f.ctx, created.ID, f.a.ID, &ticket.EditParams{Title: ptr("owner's own")})
	require.NoError(t, err)

	assert.ErrorIs(t, f.svc.RevertStewardApproval(f.ctx, f.a, got.ID), pendingaction.ErrNotRevertable)
	back, err := f.tickets.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "owner's own", back.Title)
}

func TestAnEditThatAddedTheFirstFiledPathsCannotBeReverted(t *testing.T) {
	f := newAutoFixture(t)
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "t", Status: ticket.StatusPreparing})
	require.NoError(t, err)
	got := f.stewardApproved(t, &pendingaction.ProposeParams{TicketID: created.ID, Kind: pendingaction.KindEdit,
		Payload: pendingaction.Payload{Edit: &pendingaction.EditPayload{BaseRevision: created.Revision, Paths: []string{"a.go"}}}})

	assert.ErrorIs(t, f.svc.RevertStewardApproval(f.ctx, f.a, got.ID), pendingaction.ErrNotRevertable)
}
