package ticket_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (f *fixture) edit(t *testing.T, id int64, title, body string) {
	t.Helper()
	_, err := f.svc.Edit(f.ctx, id, f.human, &ticket.EditParams{Title: &title, Body: &body})
	require.NoError(t, err)
}

func (f *fixture) createWithBody(t *testing.T, title, body, path string) *ticket.Ticket {
	t.Helper()
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: title, Body: body, Paths: []model.Path{{Path: path}}})
	require.NoError(t, err)
	return created
}

func TestSnapshot_FirstClaimFreezesTheTicketAndLaterChangesDoNotShow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	original := f.agent(t, "original")
	replayer := f.agent(t, "replayer")
	tk := f.createWithBody(t, "Original title", "Original body", "a/")
	f.comment(t, tk.ID, "before the claim")
	_, err := f.store().Snapshot(f.ctx, tk.ID)
	require.ErrorIs(t, err, ticket.ErrNoSnapshot)

	require.NotNil(t, f.claim(t, original))
	snap, err := f.store().Snapshot(f.ctx, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.SnapshotFirstClaim, snap.Source)
	assert.Equal(t, "Original title", snap.Title)

	f.edit(t, tk.ID, "Hindsight title", "Hindsight body")
	f.comment(t, tk.ID, "after the claim")
	f.addPath(t, tk.ID, "b/")
	require.Equal(t, 2, f.addAttempt(t, tk.ID, false))
	f.setAttemptN(t, tk.ID, 2, "replay = true")

	// The original attempt's reads are live.
	live, err := f.svc.DetailFor(f.ctx, tk.ID, original)
	require.NoError(t, err)
	assert.Equal(t, "Hindsight title", live.Ticket.Title)
	assert.Equal(t, []string{"before the claim", "after the claim"}, commentBodies(live))

	// A replay's claim and reads are the ticket as it was.
	claimed := f.claim(t, replayer)
	require.NotNil(t, claimed)
	assert.Equal(t, "Original title", claimed.Title)
	assert.Equal(t, "Original body", claimed.Body)
	assert.Equal(t, []model.Path{{Path: "a/", Source: model.PathSourceFiled}}, claimed.Paths)
	seen, err := f.svc.DetailFor(f.ctx, tk.ID, replayer)
	require.NoError(t, err)
	assert.Equal(t, "Original title", seen.Ticket.Title)
	assert.Equal(t, []string{"before the claim"}, commentBodies(seen))
	assert.Empty(t, seen.Revisions, "the edit since is hindsight too")
	assert.Len(t, live.Revisions, 1)

	// The board still sees the live ticket, and the replay's claim took no new snapshot.
	board, err := f.svc.Detail(f.ctx, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, "Hindsight title", board.Ticket.Title)
	again, err := f.store().Snapshot(f.ctx, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, snap.ID, again.ID)
}

func (f *fixture) setAttemptN(t *testing.T, ticketID int64, n int, set string) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET `+set+` WHERE ticket_id = $1 AND number = $2`, ticketID, n)
	require.NoError(t, err)
}

func TestSnapshot_ReplayKeepsItsOwnComments(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	original, replayer := f.agent(t, "original"), f.agent(t, "replayer")
	tk := f.createWithBody(t, "T", "B", "a/")
	require.NotNil(t, f.claim(t, original))
	f.addAttempt(t, tk.ID, false)
	f.setAttemptN(t, tk.ID, 2, "replay = true")
	require.NotNil(t, f.claim(t, replayer))
	f.comment(t, tk.ID, "guidance to everyone")

	_, err := f.svc.AddComment(f.ctx, tk.ID, ticket.AgentAuthor(replayer), "my note")
	require.NoError(t, err)
	seen, err := f.svc.DetailFor(f.ctx, tk.ID, replayer)
	require.NoError(t, err)
	assert.Equal(t, []string{"my note"}, commentBodies(seen), "ticket-level comments after the snapshot are hidden, its own are not")
}

func TestSnapshot_LaterClaimsOfAStartedTicketTakeNone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "a"), f.agent(t, "b")
	tk := f.createWithBody(t, "T", "B", "a/")
	// A ticket from before snapshots: started, with none.
	f.setAttemptN(t, tk.ID, 1, "started_revision = 0")
	f.addAttempt(t, tk.ID, false)
	require.NotNil(t, f.claim(t, a))
	require.NotNil(t, f.claim(t, b))
	_, err := f.store().Snapshot(f.ctx, tk.ID)
	assert.ErrorIs(t, err, ticket.ErrNoSnapshot, "a snapshot taken late would already show what was learnt")
}

func TestSnapshot_ParkedTicketIsRebuiltAsItWasWhenParked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.createWithBody(t, "V0", "body 0", "a/")
	f.comment(t, tk.ID, "early")
	f.edit(t, tk.ID, "V1", "body 1")
	_, err := f.svc.Update(f.ctx, tk.ID, &ticket.UpdateParams{Status: ptr(ticket.StatusParked)})
	require.NoError(t, err)
	f.edit(t, tk.ID, "V2", "body 2")
	f.comment(t, tk.ID, "late")
	f.addPath(t, tk.ID, "later/")

	// Place the edits and comments either side of the parking.
	var parkedAt time.Time
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT changed_at FROM ticket_status_changes WHERE ticket_id = $1 AND to_status = 'parked'`, tk.ID).Scan(&parkedAt))
	before, after := parkedAt.Add(-time.Hour), parkedAt.Add(time.Hour)
	f.sql(t, `UPDATE ticket_revisions SET created_at = $2 WHERE ticket_id = $1 AND revision = 1`, tk.ID, before)
	f.sql(t, `UPDATE ticket_revisions SET created_at = $2 WHERE ticket_id = $1 AND revision = 2`, tk.ID, after)
	f.sql(t, `UPDATE ticket_comments SET created_at = $2 WHERE ticket_id = $1 AND body = 'early'`, tk.ID, before)
	f.sql(t, `UPDATE ticket_comments SET created_at = $2 WHERE ticket_id = $1 AND body = 'late'`, tk.ID, after)
	f.sql(t, `UPDATE ticket_paths SET created_at = $2 WHERE ticket_id = $1 AND path = 'later/'`, tk.ID, after)

	snap, err := f.store().SnapshotParked(f.ctx, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.SnapshotRebuilt, snap.Source)
	assert.Equal(t, 1, snap.Revision)
	assert.Equal(t, "V1", snap.Title)
	assert.Equal(t, "body 1", snap.Body)
	assert.Equal(t, []model.Path{{Path: "a/", Source: model.PathSourceFiled}}, snap.Paths)
	var early int64
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT id FROM ticket_comments WHERE body = 'early'`).Scan(&early))
	assert.Equal(t, early, snap.CommentsThroughID)

	_, err = f.store().SnapshotParked(f.ctx, tk.ID)
	assert.ErrorIs(t, err, ticket.ErrSnapshotExists)
}

func TestSnapshot_BeforeAnyEditTheOriginalTextIsRebuilt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.createWithBody(t, "V0", "body 0", "a/")
	f.edit(t, tk.ID, "V1", "body 1")
	_, err := f.svc.Update(f.ctx, tk.ID, &ticket.UpdateParams{Status: ptr(ticket.StatusParked)})
	require.NoError(t, err)
	f.sql(t, `UPDATE ticket_revisions SET created_at = now() + interval '1 hour' WHERE ticket_id = $1`, tk.ID)

	snap, err := f.store().SnapshotParked(f.ctx, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, snap.Revision)
	assert.Equal(t, "V0", snap.Title)
	assert.Equal(t, "body 0", snap.Body)
}

func TestSnapshot_FreezeNowRetakesAndKeepsTheAudit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.createWithBody(t, "V0", "body 0", "a/")
	_, err := f.svc.Update(f.ctx, tk.ID, &ticket.UpdateParams{Status: ptr(ticket.StatusParked)})
	require.NoError(t, err)
	first, err := f.store().SnapshotParked(f.ctx, tk.ID)
	require.NoError(t, err)

	f.edit(t, tk.ID, "V1", "body 1")
	frozen, err := f.store().FreezeNow(f.ctx, tk.ID, f.human)
	require.NoError(t, err)
	assert.Equal(t, ticket.SnapshotRefrozen, frozen.Source)
	assert.Equal(t, f.human, *frozen.TakenBy)
	assert.Equal(t, "V1", frozen.Title)
	assert.WithinDuration(t, time.Now(), frozen.AsOf, time.Minute)

	current, err := f.store().Snapshot(f.ctx, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, frozen.ID, current.ID)
	var rows int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM ticket_snapshots WHERE ticket_id = $1`, tk.ID).Scan(&rows))
	assert.Equal(t, 2, rows, "the first snapshot %d stays", first.ID)
}

func TestSnapshot_OnlyAParkedTicketIsSnapshottedByHand(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.createWithBody(t, "T", "B", "a/")
	_, err := f.store().SnapshotParked(f.ctx, tk.ID)
	assert.ErrorIs(t, err, ticket.ErrNotParked)
	_, err = f.store().FreezeNow(f.ctx, tk.ID, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotParked)
}

func (f *fixture) sql(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, sql, args...)
	require.NoError(t, err)
}

func TestSnapshot_AReplayOfATicketWithNoneCannotTakeOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "a")
	tk := f.createWithBody(t, "T", "B", "a/")
	f.setAttemptN(t, tk.ID, 1, "replay = true")
	_, err := f.svc.Claim(f.ctx, a, nil)
	require.ErrorIs(t, err, ticket.ErrNoSnapshot, "a replay is shown the snapshot, so a ticket without one is not replayable")
	var rows int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM ticket_snapshots`).Scan(&rows))
	assert.Zero(t, rows, "a replay's claim must not freeze the ticket as it is now")
}
