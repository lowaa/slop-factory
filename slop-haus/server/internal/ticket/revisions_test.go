package ticket_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func ptr[T any](v T) *T { return &v }

func (f *fixture) revisions(t *testing.T, id int64) []ticket.Revision {
	t.Helper()
	revisions, err := f.svc.ListRevisions(f.ctx, id)
	require.NoError(t, err)
	return revisions
}

func TestEdit_RecordsTheFullOldAndNewTextOfWhatChanged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Delete button", Body: "Add a delete button."})
	require.NoError(t, err)
	assert.Equal(t, 0, created.Revision)

	edited, err := f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{Title: ptr("Reinstall script")})
	require.NoError(t, err)
	assert.True(t, edited.Changed)
	assert.Equal(t, "Reinstall script", edited.Ticket.Title)
	assert.Equal(t, "Add a delete button.", edited.Ticket.Body, "a field left out is left alone")
	assert.Equal(t, 1, edited.Ticket.Revision)

	edited, err = f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{
		Title: ptr("Reinstall script"), // unchanged, so not part of the revision
		Body:  ptr("A per-agent reinstall script."),
	})
	require.NoError(t, err)
	assert.Equal(t, 2, edited.Ticket.Revision)

	revisions := f.revisions(t, created.ID)
	require.Len(t, revisions, 2)

	first := revisions[0]
	assert.Equal(t, 1, first.Number)
	assert.Equal(t, created.ID, first.TicketID)
	require.NotNil(t, first.Editor.UserID)
	assert.Equal(t, f.human, *first.Editor.UserID)
	assert.Nil(t, first.Editor.AgentID)
	assert.Equal(t, ptr("Delete button"), first.OldTitle)
	assert.Equal(t, ptr("Reinstall script"), first.NewTitle)
	assert.Nil(t, first.OldBody, "the body was not edited")
	assert.Nil(t, first.NewBody)

	second := revisions[1]
	assert.Equal(t, 2, second.Number)
	assert.Nil(t, second.OldTitle, "a title sent unchanged is not recorded as a change")
	assert.Equal(t, ptr("Add a delete button."), second.OldBody)
	assert.Equal(t, ptr("A per-agent reinstall script."), second.NewBody)
}

func TestEdit_ThatChangesNothingRecordsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Same")

	edited, err := f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{Title: ptr("Same"), Body: ptr("")})
	require.NoError(t, err)
	assert.False(t, edited.Changed)
	assert.Equal(t, 0, edited.Ticket.Revision)
	assert.Empty(t, f.revisions(t, created.ID))
}

func TestEdit_HoldsTheTitleToTheRuleCreationDoes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Keep me")

	for _, title := range []string{"", "   \n"} {
		_, err := f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{Title: ptr(title)})
		require.ErrorIs(t, err, ticket.ErrEmptyTitle, "%q", title)

		_, err = f.svc.Create(f.ctx, &ticket.CreateParams{Title: title})
		require.ErrorIs(t, err, ticket.ErrEmptyTitle, "creation: %q", title)
	}

	_, err := f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{})
	require.ErrorIs(t, err, ticket.ErrEmptyEdit)

	// An empty body is a legitimate edit: a ticket may have no description.
	_, err = f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{Body: ptr("")})
	require.NoError(t, err)

	assert.Equal(t, "Keep me", f.get(t, created.ID).Title)
}

func TestEdit_AgainstAStaleRevisionIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Original")

	_, err := f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{Title: ptr("First"), BaseRevision: ptr(0)})
	require.NoError(t, err)

	_, err = f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{Title: ptr("Second"), BaseRevision: ptr(0)})
	require.ErrorIs(t, err, ticket.ErrStaleRevision, "opened at 0, but someone saved revision 1 since")
	assert.Equal(t, "First", f.get(t, created.ID).Title, "and nothing was overwritten")

	_, err = f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{Title: ptr("Second"), BaseRevision: ptr(1)})
	require.NoError(t, err)
}

func TestEdit_MissingTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	_, err := f.svc.Edit(f.ctx, 9999, f.human, &ticket.EditParams{Title: ptr("x")})
	require.ErrorIs(t, err, ticket.ErrNotFound)
	_, err = f.svc.ListRevisions(f.ctx, 9999)
	require.ErrorIs(t, err, ticket.ErrNotFound)
}

func TestEditHeld_EditsOnlyTheTicketTheAgentHolds(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	held := f.ticket(t, "Mine", "server/")
	other := f.ticket(t, "Someone else's", "frontend/")
	require.Equal(t, held.ID, f.claim(t, a).ID)

	edited, err := f.svc.EditHeld(f.ctx, a, &ticket.EditParams{Title: ptr("Mine, renamed"), Body: ptr("Now I know more.")})
	require.NoError(t, err)
	assert.True(t, edited.Changed)
	assert.Equal(t, held.ID, edited.Ticket.ID)
	assert.Equal(t, "Mine, renamed", edited.Ticket.Title)
	assert.Equal(t, "Someone else's", f.get(t, other.ID).Title)

	revisions := f.revisions(t, held.ID)
	require.Len(t, revisions, 1)
	require.NotNil(t, revisions[0].Editor.AgentID, "attributed to the agent")
	assert.Equal(t, a, *revisions[0].Editor.AgentID)
	assert.Nil(t, revisions[0].Editor.UserID)
	assert.Empty(t, f.revisions(t, other.ID))
}

func TestEditHeld_NeedsALease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Unclaimed")

	_, err := f.svc.EditHeld(f.ctx, a, &ticket.EditParams{Title: ptr("Hijacked")})
	require.ErrorIs(t, err, ticket.ErrNoLease)
}

// A human taking the ticket back ends the agent's right to edit it, in the
// same moment: the edit resolves the lease under the claim lock the
// revocation takes.
func TestEditHeld_AfterAHumanTookTheTicketBack(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	held := f.ticket(t, "Mine", "server/")
	f.claim(t, a)
	_, err := f.setStatus(t, held.ID, ticket.StatusOpen)
	require.NoError(t, err)

	_, err = f.svc.EditHeld(f.ctx, a, &ticket.EditParams{Title: ptr("Too late")})
	require.ErrorIs(t, err, ticket.ErrNoLease)
	assert.Equal(t, "Mine", f.get(t, held.ID).Title)
	assert.Empty(t, f.revisions(t, held.ID))
}

// Two edits racing must each record the text it actually replaced. The
// barrier holds the first edit between reading the old text and writing
// the new; the second must then wait for it rather than read the same old
// text, or the history shows an edit changing text it never saw.
func TestEdit_ConcurrentEditsEachRecordWhatTheyReplaced(t *testing.T) {
	f := newFixture(t)
	created := f.ticket(t, "Original")

	firstRead, releaseFirst := make(chan struct{}), make(chan struct{})
	var entered atomic.Int32
	restore := ticket.SetEditBarrier(func() {
		if entered.Add(1) == 1 {
			close(firstRead)
			<-releaseFirst
		}
	})
	defer restore()

	edit := func(title string) <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{Title: ptr(title)})
			done <- err
		}()
		return done
	}

	firstDone := edit("From the first")
	<-firstRead
	secondDone := edit("From the second")

	// Let the first edit go once the second is either waiting on the row
	// lock (correct) or has already finished around it (the bug). Both are
	// observed states, not a sleep standing in for one.
	var secondErr error
	secondFinished := false
	deadline := time.Now().Add(10 * time.Second)
	for !secondFinished && f.lockWaiters(t) == 0 {
		select {
		case secondErr = <-secondDone:
			secondFinished = true
		case <-time.After(5 * time.Millisecond):
		}
		require.True(t, time.Now().Before(deadline), "the second edit neither waited nor finished")
	}
	close(releaseFirst)

	require.NoError(t, <-firstDone)
	if !secondFinished {
		secondErr = <-secondDone
	}
	require.NoError(t, secondErr)

	revisions := f.revisions(t, created.ID)
	require.Len(t, revisions, 2)
	assert.Equal(t, ptr("Original"), revisions[0].OldTitle)
	assert.Equal(t, revisions[0].NewTitle, revisions[1].OldTitle,
		"the second edit replaced what the first wrote, and says so")
}

func (f *fixture) lockWaiters(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.Global().QueryRow(f.ctx, `
		SELECT count(*) FROM pg_stat_activity
		 WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&n))
	return n
}

func (f *fixture) get(t *testing.T, id int64) *ticket.Ticket {
	t.Helper()
	got, err := f.svc.Get(f.ctx, id)
	require.NoError(t, err)
	return got
}

// The board's edit over the wire: attributed to the signed-in user, a
// stale editor refused with a 409, and the history served with a diff.
func TestTicketRevisions_Wire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "Old title", Body: "one\ntwo\nthree"})
	require.NoError(t, err)
	path := fmt.Sprintf("/api/tickets/%d/revisions", created.ID)

	rec := send(t, mux, http.MethodPost, path, `{"title": "New title", "body": "one\n2\nthree", "base_revision": 0}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeTicket(t, rec.Body.Bytes())
	assert.Equal(t, "New title", got.Title)
	assert.Equal(t, 1, got.Revision)

	rec = send(t, mux, http.MethodPost, path, `{"title": "Clobber", "base_revision": 0}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	rec = send(t, mux, http.MethodPost, path, `{"title": " "}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	rec = send(t, mux, http.MethodPost, "/api/tickets/9999/revisions", `{"title": "x"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	// The PATCH route no longer carries title or body: a history-less
	// second way to change them would make the history a sample.
	for _, field := range []string{"title", "body"} {
		rec = send(t, mux, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", created.ID),
			fmt.Sprintf(`{%q: "Sneaky", "priority": "high"}`, field))
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	}
	assert.Equal(t, "New title", f.get(t, created.ID).Title)
	assert.Equal(t, "one\n2\nthree", f.get(t, created.ID).Body)

	rec = send(t, mux, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var listed struct {
		Revisions []ticket.RevisionBody `json:"revisions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
	require.Len(t, listed.Revisions, 1)
	r := listed.Revisions[0]
	assert.Equal(t, 1, r.Revision)
	require.NotNil(t, r.EditorUserID)
	assert.Equal(t, f.human.String(), *r.EditorUserID)
	assert.Nil(t, r.EditorAgentID)
	assert.Equal(t, ptr("Old title"), r.OldTitle)
	assert.Equal(t, ptr("New title"), r.NewTitle)
	assert.Equal(t, ptr("one\ntwo\nthree"), r.OldBody, "the full text, not a diff")
	assert.Equal(t, ptr("one\n2\nthree"), r.NewBody)
	assert.Equal(t, []ticket.DiffLineBody{
		{Op: "equal", Text: "one"},
		{Op: "delete", Text: "two"},
		{Op: "insert", Text: "2"},
		{Op: "equal", Text: "three"},
	}, r.BodyDiff)
}
