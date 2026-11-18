package attemptverdict_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptverdict"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type fixture struct {
	database *db.DB
	ctx      context.Context
	store    *attemptverdict.Store
	svc      *attemptverdict.Service
}

func newFixture(t *testing.T, database *db.DB) *fixture {
	t.Helper()
	store := attemptverdict.NewStore(database)
	return &fixture{
		database: database, store: store, svc: attemptverdict.NewService(store, nil),
		ctx: db.WithTenant(context.Background(), dbtest.NewTenant(t, database)),
	}
}

func (f *fixture) person(t *testing.T) *authn.User {
	t.Helper()
	tenant, _ := db.TenantFrom(f.ctx)
	return &authn.User{ID: dbtest.NewUser(t, f.database), TenantID: tenant}
}

// ticketAt files a ticket whose only attempt has the status.
func (f *fixture) ticketAt(t *testing.T, status string) ticket.AttemptRef {
	t.Helper()
	var id int64
	require.NoError(t, f.database.QueryRow(f.ctx, `INSERT INTO tickets (title, status) VALUES ('t', 'open') RETURNING id`).Scan(&id))
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET status = $2 WHERE ticket_id = $1`, id, status)
	require.NoError(t, err)
	return ticket.AttemptRef{TicketID: id, Number: 1}
}

func TestSetKeepsTheHistoryOfAChangedMind(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	ref := f.ticketAt(t, "review")
	me := f.person(t)

	first, err := f.svc.Set(f.ctx, me, ref, attemptverdict.WouldMerge, "looks fine")
	require.NoError(t, err)
	assert.Equal(t, attemptverdict.WouldMerge, first.Verdict)
	assert.Equal(t, "looks fine", first.Note)
	assert.Empty(t, first.History)
	require.NotNil(t, first.SetByUserID)
	assert.Equal(t, me.ID.String(), *first.SetByUserID)

	second, err := f.svc.Set(f.ctx, me, ref, attemptverdict.WouldNotMerge, "")
	require.NoError(t, err)
	assert.Equal(t, attemptverdict.WouldNotMerge, second.Verdict)
	require.Len(t, second.History, 1)
	assert.Equal(t, attemptverdict.WouldMerge, second.History[0].Verdict)
	assert.Equal(t, "looks fine", second.History[0].Note, "the replaced verdict is kept, not overwritten")

	third, err := f.svc.Set(f.ctx, f.person(t), ref, attemptverdict.WouldMergeWithChanges, "rename it")
	require.NoError(t, err)
	assert.Equal(t, attemptverdict.WouldMergeWithChanges, third.Verdict)
	require.Len(t, third.History, 2)
	assert.Equal(t, attemptverdict.WouldNotMerge, third.History[0].Verdict, "newest first")
	assert.Equal(t, attemptverdict.WouldMerge, third.History[1].Verdict)
}

func TestAttemptVerdictsReadsOnlyTheRequestedPairs(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	me := f.person(t)
	one, other, unjudged := f.ticketAt(t, "done"), f.ticketAt(t, "closed"), f.ticketAt(t, "review")
	// A second attempt of the first ticket, judged but not asked for: the
	// read is by pair, not by ticket.
	_, err := f.database.Exec(f.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, 2, 'review')`, one.TicketID)
	require.NoError(t, err)
	second := ticket.AttemptRef{TicketID: one.TicketID, Number: 2}
	for ref, v := range map[ticket.AttemptRef]string{
		one: attemptverdict.WouldMerge, second: attemptverdict.WouldMergeWithChanges, other: attemptverdict.WouldNotMerge,
	} {
		_, err := f.svc.Set(f.ctx, me, ref, v, "")
		require.NoError(t, err)
	}

	got, err := f.store.AttemptVerdicts(f.ctx, []ticket.AttemptRef{one, unjudged})
	require.NoError(t, err)
	require.Len(t, got, 1, "the unjudged attempt is absent; the sibling attempt and the other ticket were not asked for")
	assert.Equal(t, attemptverdict.WouldMerge, got[one].Verdict)

	got, err = f.store.AttemptVerdicts(f.ctx, []ticket.AttemptRef{second, other})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, attemptverdict.WouldMergeWithChanges, got[second].Verdict)
	assert.Equal(t, attemptverdict.WouldNotMerge, got[other].Verdict)

	none, err := f.store.AttemptVerdicts(f.ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// Each setter names the verdict it replaced, so the verdicts that were
// replaced are the ones that came before the newest, whatever the order the
// setters ran in.
func TestConcurrentSettersEachReplaceADifferentVerdict(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	verdicts := []string{attemptverdict.WouldMerge, attemptverdict.WouldMergeWithChanges, attemptverdict.WouldNotMerge}
	for round := 0; round < 10; round++ {
		ref := f.ticketAt(t, "review")
		me := f.person(t)
		previous := make([]string, len(verdicts))
		var wg sync.WaitGroup
		for i, v := range verdicts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p, err := f.store.Set(f.ctx, ref, v, "", me.ID)
				assert.NoError(t, err)
				previous[i] = p
			}()
		}
		wg.Wait()

		got, err := f.store.AttemptVerdicts(f.ctx, []ticket.AttemptRef{ref})
		require.NoError(t, err)
		// Newest first: the replaced verdicts are the older two, and none is "".
		replaced := []string{"", got[ref].History[1].Verdict, got[ref].History[0].Verdict}
		assert.ElementsMatch(t, replaced, previous, "round %d", round)
	}
}

func TestSetIsAuditedWithTheVerdictItReplaced(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	var logged bytes.Buffer
	events := audit.New(slog.NewJSONHandler(&logged, nil), nil)
	svc := attemptverdict.NewService(f.store, events)
	ref := f.ticketAt(t, "review")
	me := f.person(t)

	_, err := svc.Set(f.ctx, me, ref, attemptverdict.WouldMerge, "private words")
	require.NoError(t, err)
	_, err = svc.Set(f.ctx, me, ref, attemptverdict.WouldNotMerge, "")
	require.NoError(t, err)
	_, err = svc.Set(f.ctx, me, ref, "bogus", "")
	require.Error(t, err)

	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	require.Len(t, lines, 2, "a refused set is not recorded")
	var first, second map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	assert.Equal(t, "attempt_verdict_set", first["action"])
	assert.Equal(t, me.ID.String(), first["user_id"])
	assert.EqualValues(t, ref.TicketID, first["ticket"])
	assert.EqualValues(t, 1, first["attempt"])
	assert.Equal(t, "would_merge", first["verdict"])
	assert.Equal(t, "", first["previous"])
	assert.Equal(t, true, first["has_note"])
	assert.Equal(t, "would_not_merge", second["verdict"])
	assert.Equal(t, "would_merge", second["previous"])
	assert.NotContains(t, logged.String(), "private words", "the note is not logged")
}

func TestSetRefusesAnAttemptThatHasNotFinished(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	me := f.person(t)
	for _, status := range []string{"queued", "in_progress", "discussion", "in_merge_queue"} {
		ref := f.ticketAt(t, status)
		_, err := f.svc.Set(f.ctx, me, ref, attemptverdict.WouldMerge, "")
		assert.ErrorIs(t, err, attemptverdict.ErrNotFinished, status)
	}
	for _, status := range []string{"review", "done", "closed"} {
		_, err := f.svc.Set(f.ctx, me, f.ticketAt(t, status), attemptverdict.WouldMerge, "")
		assert.NoError(t, err, status)
	}
}

func TestSetRefusesBadInputAndMissingAttempts(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	me := f.person(t)
	ref := f.ticketAt(t, "review")

	_, err := f.svc.Set(f.ctx, me, ref, "merge_it", "")
	assert.ErrorIs(t, err, attemptverdict.ErrInvalidVerdict)
	_, err = f.svc.Set(f.ctx, me, ref, attemptverdict.WouldMerge, string(make([]rune, attemptverdict.MaxNoteRunes+1)))
	assert.ErrorIs(t, err, attemptverdict.ErrNoteTooLong)
	_, err = f.svc.Set(f.ctx, me, ticket.AttemptRef{TicketID: ref.TicketID, Number: 9}, attemptverdict.WouldMerge, "")
	assert.ErrorIs(t, err, ticket.ErrNoSuchAttempt)
	_, err = f.svc.Get(f.ctx, me, ref)
	assert.ErrorIs(t, err, attemptverdict.ErrNoVerdict, "a refused set leaves nothing behind")
}

func TestAnAgentCannotSetOrReadAVerdict(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	ref := f.ticketAt(t, "review")

	_, err := f.svc.Set(f.ctx, nil, ref, attemptverdict.WouldMerge, "")
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.svc.Get(f.ctx, nil, ref)
	assert.ErrorIs(t, err, authn.ErrNotPerson)

	_, err = f.svc.Get(f.ctx, f.person(t), ref)
	assert.ErrorIs(t, err, attemptverdict.ErrNoVerdict, "nothing was written")
}

func TestVerdictsStayInTheirTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	a, b := newFixture(t, database), newFixture(t, database)
	ref := a.ticketAt(t, "review")
	_, err := a.svc.Set(a.ctx, a.person(t), ref, attemptverdict.WouldMerge, "")
	require.NoError(t, err)

	got, err := a.store.AttemptVerdicts(b.ctx, []ticket.AttemptRef{ref})
	require.NoError(t, err)
	assert.Empty(t, got, "B reads none of A's verdicts whatever refs it names")

	_, err = b.svc.Set(b.ctx, b.person(t), ref, attemptverdict.WouldNotMerge, "")
	assert.ErrorIs(t, err, ticket.ErrNoSuchAttempt, "B cannot judge A's attempt")
	got, err = a.store.AttemptVerdicts(a.ctx, []ticket.AttemptRef{ref})
	require.NoError(t, err)
	assert.Equal(t, attemptverdict.WouldMerge, got[ref].Verdict)
}
