package pendingaction_test

import (
	"context"
	"encoding/json"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func ptr[T any](v T) *T { return &v }

func (d *decider) editTarget(t *testing.T, status ticket.Status, paths ...string) *ticket.Ticket {
	t.Helper()
	filed := make([]model.Path, len(paths))
	for i, p := range paths {
		filed[i] = model.Path{Path: p, Source: model.PathSourceFiled}
	}
	created, err := d.tickets.Create(d.ctx, &ticket.CreateParams{
		Title: "old title", Body: "old body", Priority: model.PriorityNormal, Status: ticket.StatusPreparing, Paths: filed,
	})
	require.NoError(t, err)
	if status != ticket.StatusPreparing {
		d.exec(t, `UPDATE tickets SET status = $2 WHERE id = $1`, created.ID, string(status))
	}
	return created
}

func (d *decider) proposeEdit(t *testing.T, id int64, edit *pendingaction.EditPayload) (*pendingaction.Action, error) {
	t.Helper()
	return d.svc.Propose(d.ctx, &pendingaction.ProposeParams{
		AgentID: d.console, TicketID: id, Kind: pendingaction.KindEdit, Payload: pendingaction.Payload{Edit: edit},
	})
}

func (d *decider) mustProposeEdit(t *testing.T, id int64, edit *pendingaction.EditPayload) *pendingaction.Action {
	t.Helper()
	a, err := d.proposeEdit(t, id, edit)
	require.NoError(t, err)
	return a
}

func TestApprovingAnEditAppliesItAsThePerson(t *testing.T) {
	d := newDecider(t)
	target := d.editTarget(t, ticket.StatusPreparing, "a/old/", "keep.go")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{
		BaseRevision: target.Revision,
		Title:        ptr("new title"), Body: ptr("new body"),
		Paths: []string{"keep.go", "./a/new/"}, Priority: ptr(model.PriorityHigh),
	})
	assert.Equal(t, []string{"keep.go", "a/new/"}, p.Payload.Edit.Paths, "stored normalised")

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateApproved, settled.State)

	got, err := d.tickets.Get(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, "new title", got.Title)
	assert.Equal(t, "new body", got.Body)
	assert.Equal(t, model.PriorityHigh, got.Priority)
	assert.Equal(t, ticket.StatusPreparing, got.Status, "an edit never moves the ticket")
	var paths []string
	for _, p := range got.Paths {
		paths = append(paths, p.Path)
	}
	assert.ElementsMatch(t, []string{"keep.go", "a/new/"}, paths)

	revisions, err := d.tickets.ListRevisions(d.ctx, target.ID)
	require.NoError(t, err)
	require.Len(t, revisions, 1)
	require.NotNil(t, revisions[0].Editor.UserID, "the person's edit, not the console's")
	assert.Equal(t, d.approver.ID, *revisions[0].Editor.UserID)
	assert.Nil(t, revisions[0].Editor.AgentID)
	assert.Equal(t, "old title", *revisions[0].OldTitle)
	assert.Equal(t, "new body", *revisions[0].NewBody)
}

func TestAnEditOfOnlyPrioritySkipsTheTextHistory(t *testing.T) {
	d := newDecider(t)
	target := d.editTarget(t, ticket.StatusOpen, "a/")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{BaseRevision: target.Revision, Priority: ptr(model.PriorityUrgent)})

	_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)
	got, err := d.tickets.Get(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PriorityUrgent, got.Priority)
	revisions, err := d.tickets.ListRevisions(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Empty(t, revisions)
}

func TestApprovingAnEditOfATicketEditedSinceIsRefusedAsStale(t *testing.T) {
	d := newDecider(t)
	target := d.editTarget(t, ticket.StatusPreparing, "a/")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{
		BaseRevision: target.Revision, Body: ptr("a rewrite of the body"), Priority: ptr(model.PriorityHigh),
	})
	_, err := d.tickets.Edit(d.ctx, target.ID, d.approver.ID, &ticket.EditParams{Body: ptr("what the person wrote meanwhile")})
	require.NoError(t, err)

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Equal(t, pendingaction.StateStale, settled.State)
	got, err := d.tickets.Get(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, "what the person wrote meanwhile", got.Body, "the later edit is not overwritten")
	assert.Equal(t, model.PriorityNormal, got.Priority, "nor is any of the proposal applied")
}

func TestAnEditIsRefusedWhenProposed(t *testing.T) {
	d := newDecider(t)
	target := d.editTarget(t, ticket.StatusPreparing, "a/")
	_, err := d.tickets.Edit(d.ctx, target.ID, d.approver.ID, &ticket.EditParams{Body: ptr("edited")})
	require.NoError(t, err)
	done := d.editTarget(t, ticket.StatusDone, "b/")
	cancelled := d.editTarget(t, ticket.StatusCancelled, "c/")

	tests := map[string]struct {
		ticket int64
		edit   *pendingaction.EditPayload
		want   string
	}{
		"base revision behind": {target.ID, &pendingaction.EditPayload{BaseRevision: target.Revision, Body: ptr("x")}, "revision 1"},
		"done":                 {done.ID, &pendingaction.EditPayload{BaseRevision: done.Revision, Body: ptr("x")}, "done"},
		"cancelled":            {cancelled.ID, &pendingaction.EditPayload{BaseRevision: cancelled.Revision, Body: ptr("x")}, "cancelled"},
		"empty paths":          {done.ID, &pendingaction.EditPayload{Paths: []string{}}, "no paths"},
		"nothing to change":    {target.ID, &pendingaction.EditPayload{BaseRevision: 1}, "at least one"},
		"blank title":          {target.ID, &pendingaction.EditPayload{BaseRevision: 1, Title: ptr("  ")}, "blank"},
		"unknown priority":     {target.ID, &pendingaction.EditPayload{BaseRevision: 1, Priority: ptr(model.Priority("asap"))}, "priority"},
		"bad path":             {target.ID, &pendingaction.EditPayload{BaseRevision: 1, Paths: []string{"../escape"}}, "escape"},
		"overlapping paths":    {target.ID, &pendingaction.EditPayload{BaseRevision: 1, Paths: []string{"a/", "a/b.go"}}, "overlaps"},
		"no edit at all":       {target.ID, nil, "at least one"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := d.proposeEdit(t, tc.ticket, tc.edit)
			require.ErrorIs(t, err, pendingaction.ErrInvalid)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	assert.Zero(t, d.count(t))
}

func TestAnEditNeedsAnEnabledConsoleAndATicketOfTheTenant(t *testing.T) {
	d := newDecider(t)
	other := dbtest.NewTenant(t, d.database)
	foreign, err := d.tickets.Create(db.WithTenant(d.ctx, other), &ticket.CreateParams{Title: "theirs", Status: ticket.StatusPreparing})
	require.NoError(t, err)

	_, err = d.proposeEdit(t, foreign.ID, &pendingaction.EditPayload{BaseRevision: 0, Body: ptr("x")})
	require.ErrorIs(t, err, pendingaction.ErrTicketNotFound)

	target := d.editTarget(t, ticket.StatusPreparing, "a/")
	_, err = d.svc.Propose(d.ctx, &pendingaction.ProposeParams{
		AgentID: d.newAgent(t, "worker", false), TicketID: target.ID, Kind: pendingaction.KindEdit,
		Payload: pendingaction.Payload{Edit: &pendingaction.EditPayload{Body: ptr("x")}},
	})
	require.ErrorIs(t, err, pendingaction.ErrNotConsole)
	assert.Zero(t, d.count(t))
}

func TestAnEditOfATicketAnAgentHoldsIsRefusedWhenProposedAndWhenApproved(t *testing.T) {
	d := newDecider(t)
	worker := d.newAgent(t, "worker", false)

	held := d.editTarget(t, ticket.StatusOpen, "a/")
	d.exec(t, `UPDATE tickets SET status = 'open' WHERE id = $1`, held.ID)
	claimed, err := d.tickets.Claim(d.ctx, worker, &held.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket)
	_, err = d.proposeEdit(t, held.ID, &pendingaction.EditPayload{BaseRevision: held.Revision, Body: ptr("x")})
	require.ErrorIs(t, err, pendingaction.ErrTicketHeld)

	later := d.editTarget(t, ticket.StatusOpen, "b/")
	p := d.mustProposeEdit(t, later.ID, &pendingaction.EditPayload{BaseRevision: later.Revision, Body: ptr("x")})
	_, err = d.tickets.Claim(d.ctx, d.newAgent(t, "second worker", false), &later.ID)
	require.NoError(t, err)
	d.exec(t, `UPDATE tickets SET status = 'open' WHERE id = $1`, later.ID)
	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Contains(t, settled.Outcome, "holds the ticket")
	got, err := d.tickets.Get(d.ctx, later.ID)
	require.NoError(t, err)
	assert.Equal(t, "old body", got.Body)
}

func TestApprovingAnEditThatTakesPathsAHeldTicketHoldsIsRefusedAndChangesNothing(t *testing.T) {
	d := newDecider(t)
	worker := d.newAgent(t, "worker", false)
	holder := d.editTarget(t, ticket.StatusOpen, "shared/")
	_, err := d.tickets.Claim(d.ctx, worker, &holder.ID)
	require.NoError(t, err)

	target := d.editTarget(t, ticket.StatusOpen, "mine/")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{
		BaseRevision: target.Revision, Title: ptr("renamed"), Paths: []string{"shared/x.go"},
	})
	// An open ticket holds no scope until claimed; put it in review, which does.
	d.exec(t, `UPDATE tickets SET status = 'review' WHERE id = $1`, target.ID)
	d.exec(t, `UPDATE pending_actions SET base_status = 'review' WHERE id = $1`, p.ID)

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrRefused)
	var conflict *ticket.ScopeConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, pendingaction.StateFailed, settled.State)
	got, err := d.tickets.Get(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, "old title", got.Title, "the text edit rolled back with the paths")
	assert.Equal(t, model.PriorityNormal, got.Priority)
	require.Len(t, got.Paths, 1)
	assert.Equal(t, "mine/", got.Paths[0].Path, "and the paths are as they were")
	assert.Equal(t, target.Revision, got.Revision)
	revisions, err := d.tickets.ListRevisions(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Empty(t, revisions)
}

func TestApprovingAnEditOfPathsOrPriorityChangedSinceIsRefusedAsStale(t *testing.T) {
	d := newDecider(t)
	tests := map[string]struct {
		edit   func(base int) *pendingaction.EditPayload
		change func(t *testing.T, id int64)
		want   string
	}{
		"priority": {
			edit: func(base int) *pendingaction.EditPayload {
				return &pendingaction.EditPayload{BaseRevision: base, Priority: ptr(model.PriorityHigh)}
			},
			change: func(t *testing.T, id int64) {
				_, err := d.tickets.Update(d.ctx, id, &ticket.UpdateParams{Priority: ptr(model.PriorityLow)})
				require.NoError(t, err)
			},
			want: "priority",
		},
		"paths": {
			edit: func(base int) *pendingaction.EditPayload {
				return &pendingaction.EditPayload{BaseRevision: base, Paths: []string{"b/"}}
			},
			change: func(t *testing.T, id int64) {
				_, err := d.tickets.Update(d.ctx, id, &ticket.UpdateParams{Paths: []model.Path{{Path: "c/"}}})
				require.NoError(t, err)
			},
			want: "paths",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			target := d.editTarget(t, ticket.StatusPreparing, "a/")
			p := d.mustProposeEdit(t, target.ID, tc.edit(target.Revision))
			tc.change(t, target.ID)
			before, err := d.tickets.Get(d.ctx, target.ID)
			require.NoError(t, err)

			v, err := d.svc.ViewOf(d.ctx, testPerson(), p.ID)
			require.NoError(t, err)
			assert.Contains(t, v.StaleReason, tc.want, "the card says so before it is approved")

			settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
			require.ErrorIs(t, err, pendingaction.ErrStale)
			assert.Contains(t, settled.Outcome, tc.want)
			after, err := d.tickets.Get(d.ctx, target.ID)
			require.NoError(t, err)
			assert.Equal(t, before.Priority, after.Priority)
			assert.Equal(t, before.Paths, after.Paths, "what the person set meanwhile is not overwritten")
		})
	}
}

func TestAPendingEditViewShowsWhatItWouldChange(t *testing.T) {
	d := newDecider(t)
	target := d.editTarget(t, ticket.StatusPreparing, "a/old/", "keep.go")
	neighbour := d.editTarget(t, ticket.StatusOpen, "a/new/deep.go")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{
		BaseRevision: target.Revision, Title: ptr("old title"), Body: ptr("new body"),
		Paths: []string{"keep.go", "a/new/"}, Priority: ptr(model.PriorityHigh),
	})

	v, err := d.svc.ViewOf(d.ctx, testPerson(), p.ID)
	require.NoError(t, err)
	require.NotNil(t, v.Edit)
	assert.Nil(t, v.Edit.Title, "an unchanged title is not a change")
	require.NotNil(t, v.Edit.Body)
	assert.Equal(t, "old body", *v.Edit.Body.Old)
	assert.Equal(t, "new body", v.Edit.Body.New)
	assert.Equal(t, model.PriorityNormal, *v.Edit.Priority.Old)
	assert.Equal(t, []string{"a/new/"}, v.Edit.PathsAdded)
	assert.Equal(t, []string{"a/old/"}, v.Edit.PathsRemoved)
	require.Len(t, v.Edit.Overlaps, 1)
	assert.Equal(t, neighbour.ID, v.Edit.Overlaps[0].TicketID)

	_, err = d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)
	decided, err := d.svc.ViewOf(d.ctx, testPerson(), p.ID)
	require.NoError(t, err)
	require.NotNil(t, decided.Edit.Body)
	assert.Nil(t, decided.Edit.Body.Old, "a decided edit has no ticket to compare to")
	assert.Empty(t, decided.Edit.PathsAdded)
}

func TestAnEditProposalIsNotOfferedToApproveAll(t *testing.T) {
	d := newDecider(t)
	_, err := d.svc.ApproveAll(d.ctx, d.approver, pendingaction.KindEdit, []uuid.UUID{})
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
}

func TestTheRoutesShowAnEditAsWhatItWouldChange(t *testing.T) {
	w := newWire(t)
	target := w.editTarget(t, ticket.StatusPreparing, "a/old/")
	neighbour := w.editTarget(t, ticket.StatusOpen, "a/new/")
	p := w.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{
		BaseRevision: target.Revision, Body: ptr("old body\nsecond line"), Paths: []string{"a/new/"}, Priority: ptr(model.PriorityUrgent),
	})

	rec := w.as(t, userToken, http.MethodGet, "/api/proposals/"+p.ID.String(), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got pendingaction.ProposalBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "edit", got.Kind)
	require.NotNil(t, got.Edit)
	assert.Nil(t, got.Edit.Title)
	require.NotNil(t, got.Edit.Body)
	assert.Equal(t, []ticket.DiffLineBody{{Op: "equal", Text: "old body"}, {Op: "insert", Text: "second line"}}, got.Edit.Body.Diff)
	assert.Equal(t, []string{"a/new/"}, got.Edit.PathsAdded)
	assert.Equal(t, []string{"a/old/"}, got.Edit.PathsRemoved)
	assert.Equal(t, "urgent", got.Edit.Priority.New)
	require.Len(t, got.Edit.Overlaps, 1)
	assert.Equal(t, neighbour.ID, got.Edit.Overlaps[0].TicketID)

	rec = w.as(t, userToken, http.MethodPost, "/api/proposals/approve_all", `{"kind": "edit"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "an edit is approved on its own")
	assert.Equal(t, pendingaction.StatePending, w.stateOf(t, p).State)
}

func TestApprovingAnEditThatOverlapsALiveTicketNobodyHoldsStillApplies(t *testing.T) {
	d := newDecider(t)
	d.editTarget(t, ticket.StatusOpen, "shared/")
	target := d.editTarget(t, ticket.StatusPreparing, "mine/")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{BaseRevision: target.Revision, Paths: []string{"shared/x.go"}})

	v, err := d.svc.ViewOf(d.ctx, testPerson(), p.ID)
	require.NoError(t, err)
	require.Len(t, v.Edit.Overlaps, 1, "shown on the card")

	_, err = d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err, "and not refused: nobody holds either scope")
	got, err := d.tickets.Get(d.ctx, target.ID)
	require.NoError(t, err)
	require.Len(t, got.Paths, 1)
	assert.Equal(t, "shared/x.go", got.Paths[0].Path)
}

// refusingPrepare is the ticket service whose pre-transaction check starts
// refusing paths once armed, as a workspace manifest adopted after the
// proposal would, and which counts the transactions it was asked to open.
type refusingPrepare struct {
	*ticket.Service
	armed bool
	txs   int
}

func (r *refusingPrepare) PrepareUpdate(ctx context.Context, params *ticket.UpdateParams) error {
	if r.armed && params.Paths != nil {
		return ticket.ErrScopesSpanRepos
	}
	return r.Service.PrepareUpdate(ctx, params)
}

func (r *refusingPrepare) InTransition(ctx context.Context, fn func(tx ticket.TransitionTx) error) error {
	r.txs++
	return r.Service.InTransition(ctx, fn)
}

func TestApprovingAnEditWhoseNewPathsAreRefusedBeforeTheTransactionIsRecordedAsFailed(t *testing.T) {
	d := newDecider(t)
	tickets := &refusingPrepare{Service: d.tickets}
	d.svc = pendingaction.NewService(pendingaction.NewPostgresStore(d.database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(tickets, d.grants))
	d.svc.SetNow(d.clock.Now)
	target := d.editTarget(t, ticket.StatusPreparing, "a/")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{BaseRevision: target.Revision, Title: ptr("renamed"), Paths: []string{"b/"}})
	tickets.armed = true

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrRefused)
	require.ErrorIs(t, err, ticket.ErrScopesSpanRepos)
	assert.Equal(t, pendingaction.StateFailed, settled.State)
	assert.Zero(t, tickets.txs, "refused before a transaction was opened")
	got, err := d.tickets.Get(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, "old title", got.Title)
}

// With the tenant's scope locking off a real overlap blocks nothing, and the
// approval card says so rather than showing a plain conflict.
func TestAPendingEditCardMarksOverlapsAsBlockingNothingWhenLockingIsOff(t *testing.T) {
	d := newDecider(t)
	target := d.editTarget(t, ticket.StatusPreparing, "a/old/")
	d.editTarget(t, ticket.StatusOpen, "a/new/deep.go")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{BaseRevision: target.Revision, Paths: []string{"a/new/"}})
	_, err := d.database.Exec(d.ctx, `
		INSERT INTO tenant_settings (setting_name, setting_type, bool_value) VALUES ($1, 'bool', false)`,
		settings.TicketScopeLocking)
	require.NoError(t, err)

	v, err := d.svc.ViewOf(d.ctx, testPerson(), p.ID)
	require.NoError(t, err)

	got := pendingaction.ToBody(v).Edit.Overlaps
	require.Len(t, got, 1)
	assert.True(t, got[0].LockingOff)
}
