package ticket_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) search(t *testing.T, filter *ticket.ListFilter) []int64 {
	t.Helper()
	found, err := f.svc.List(f.ctx, filter)
	require.NoError(t, err)
	ids := make([]int64, 0, len(found))
	for _, tk := range found {
		ids = append(ids, tk.ID)
	}
	return ids
}

func (f *fixture) searchText(t *testing.T, text string) []int64 {
	t.Helper()
	return f.search(t, &ticket.ListFilter{Text: text})
}

func (f *fixture) ticketWithBody(t *testing.T, title, body string) *ticket.Ticket {
	t.Helper()
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: title, Body: body})
	require.NoError(t, err)
	return created
}

func (f *fixture) comment(t *testing.T, ticketID int64, body string) {
	t.Helper()
	_, err := f.svc.AddComment(f.ctx, ticketID, ticket.UserAuthor(f.human), body)
	require.NoError(t, err)
}

// Each of the four places a ticket's text lives is searched, and a ticket
// with the word in none of them is not returned.
func TestListText_MatchesTitleBodyCommentAndHandoff(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// First, so it is the only ticket there is to claim.
	inHandoff := f.ticket(t, "Handed off")
	a := f.agent(t, "builder-1")
	require.Equal(t, inHandoff.ID, f.claim(t, a).ID)
	_, err := f.svc.Discuss(f.ctx, a, "Which shape?", "stopped at the ZEBRA parser")
	require.NoError(t, err)

	inTitle := f.ticketWithBody(t, "Fix the Zebra import", "")
	inBody := f.ticketWithBody(t, "Import fix", "the zebra importer drops rows")
	inComment := f.ticketWithBody(t, "Another import", "")
	f.comment(t, inComment.ID, "saw a zebra in the logs")
	nowhere := f.ticketWithBody(t, "Unrelated", "nothing to see")
	f.comment(t, nowhere.ID, "still nothing")

	assert.ElementsMatch(t,
		[]int64{inTitle.ID, inBody.ID, inComment.ID, inHandoff.ID},
		f.searchText(t, "zebra"),
		"a case-insensitive substring of any source matches")
}

// Every word must appear somewhere on the ticket, not all in one place:
// "importer deadlock" finds the ticket whose body says one and whose
// comment says the other.
func TestListText_EveryWordSomewhereOnTheTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	both := f.ticketWithBody(t, "Importer hangs", "")
	f.comment(t, both.ID, "looks like a deadlock")
	onlyOne := f.ticketWithBody(t, "Importer is slow", "")

	assert.Equal(t, []int64{both.ID}, f.searchText(t, "importer deadlock"))
	assert.ElementsMatch(t, []int64{both.ID, onlyOne.ID}, f.searchText(t, "  importer  "),
		"surrounding whitespace is not part of a word")
	assert.ElementsMatch(t, []int64{both.ID, onlyOne.ID}, f.searchText(t, "   "),
		"a blank filter filters nothing")
}

// %, _ and \ in what someone types are literal characters, not LIKE
// wildcards -- "100%" must not find "1000", nor "a_b" find "axb".
func TestListText_LikeMetacharactersAreLiteral(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	percent := f.ticketWithBody(t, "Coverage at 100% now", "")
	f.ticketWithBody(t, "Handles 1000 rows", "")
	underscore := f.ticketWithBody(t, "rename board_view", "")
	f.ticketWithBody(t, "rename boardXview", "")
	backslash := f.ticketWithBody(t, `path C:\temp`, "")
	f.ticketWithBody(t, `path C:temp`, "")

	assert.Equal(t, []int64{percent.ID}, f.searchText(t, "100%"))
	assert.Equal(t, []int64{underscore.ID}, f.searchText(t, "board_view"))
	assert.Equal(t, []int64{backslash.ID}, f.searchText(t, `C:\temp`))
}

// The text filter narrows the status filter rather than replacing it: the
// board's "Include done" checkbox still decides whether done tickets show.
func TestListText_CombinesWithTheStatusFilter(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	open := f.ticketWithBody(t, "zebra open", "")
	done := f.ticketWithBody(t, "zebra done", "")
	doneStatus := ticket.StatusDone
	_, err := f.svc.Update(f.ctx, done.ID, &ticket.UpdateParams{Status: &doneStatus})
	require.NoError(t, err)

	assert.Equal(t, []int64{open.ID},
		f.search(t, &ticket.ListFilter{Text: "zebra", Statuses: []ticket.Status{ticket.StatusOpen}}))
}

// Text a ticket used to say, kept in its revision history, is not what it
// says now, so it is not matched.
func TestListText_IgnoresEditedAwayText(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticketWithBody(t, "Old zebra title", "")
	newTitle := "New title"
	_, err := f.svc.Edit(f.ctx, created.ID, f.human, &ticket.EditParams{Title: &newTitle})
	require.NoError(t, err)

	assert.Empty(t, f.searchText(t, "zebra"))
	assert.Equal(t, []int64{created.ID}, f.searchText(t, "new"))
}

func TestListText_RefusesOverlongText(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	_, err := f.svc.List(f.ctx, &ticket.ListFilter{Text: strings.Repeat("é", ticket.MaxSearchTextLen+1)})
	require.ErrorIs(t, err, ticket.ErrInvalidSearch)

	// The limit is in characters, not bytes: a multi-byte one at the limit
	// is allowed.
	_, err = f.svc.List(f.ctx, &ticket.ListFilter{Text: strings.Repeat("é", ticket.MaxSearchTextLen)})
	require.NoError(t, err)
}

// The word count, not just the length, is bounded: each word is a pass
// over the tenant's text. Repeats are one word.
func TestListText_RefusesTooManyWords(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	words := func(n int) string {
		w := make([]string, n)
		for i := range w {
			w[i] = string(rune('a' + i))
		}
		return strings.Join(w, " ")
	}

	_, err := f.svc.List(f.ctx, &ticket.ListFilter{Text: words(ticket.MaxSearchTerms + 1)})
	require.ErrorIs(t, err, ticket.ErrInvalidSearch)

	_, err = f.svc.List(f.ctx, &ticket.ListFilter{Text: words(ticket.MaxSearchTerms) + " A b"})
	require.NoError(t, err, "a repeated word, in either case, is not another word")
}

// Postgres rejects a NUL or invalid UTF-8 in a text parameter outright, so
// left to the query these would be a 500 rather than a refusal.
func TestListText_RefusesTextPostgresCannotCompare(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	for _, text := range []string{"a\x00b", "a\xffb"} {
		_, err := f.svc.List(f.ctx, &ticket.ListFilter{Text: text})
		require.ErrorIs(t, err, ticket.ErrInvalidSearch, "%q", text)
	}
}

// Ticket ids are per tenant, so tenant B's #1 carrying the word in a
// comment must not surface tenant A's #1 -- the subqueries' join on
// tenant_id and RLS both have to hold.
func TestTenancy_TextSearchStaysInTheTenant(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	aTicket := a.ticket(t, "plain title")
	bTicket := b.ticket(t, "zebra in the title")
	require.Equal(t, aTicket.ID, bTicket.ID, "both tenants' first ticket is #1")
	_, err := b.svc.AddComment(b.ctx, bTicket.ID, ticket.AgentAuthor(b.agentID), "a zebra comment")
	require.NoError(t, err)

	fromA, err := a.svc.List(a.ctx, &ticket.ListFilter{Text: "zebra"})
	require.NoError(t, err)
	assert.Empty(t, fromA)

	fromB, err := b.svc.List(b.ctx, &ticket.ListFilter{Text: "zebra"})
	require.NoError(t, err)
	require.Len(t, fromB, 1)
	assert.Equal(t, bTicket.ID, fromB[0].ID)
}

// The limit is taken after the filter, newest first: a newer ticket the
// text leaves out does not use up a place.
func TestListLimit_KeepsTheNewestMatches(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ticketWithBody(t, "zebra oldest", "")
	middle := f.ticketWithBody(t, "zebra middle", "")
	newest := f.ticketWithBody(t, "zebra newest", "")
	f.ticketWithBody(t, "unrelated, and newer still", "")

	assert.Equal(t, []int64{newest.ID, middle.ID}, f.search(t, &ticket.ListFilter{Text: "zebra", Limit: 2}))
	assert.Len(t, f.search(t, &ticket.ListFilter{Text: "zebra"}), 3, "zero is no limit")
}

func TestListLimit_RefusesANegativeLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.List(f.ctx, &ticket.ListFilter{Limit: -1})
	assert.ErrorIs(t, err, ticket.ErrInvalidSearch)
}

// "#<id>" finds that ticket even when no text anywhere says "#<id>", and
// still finds tickets whose text does; every word must still match.
func TestListText_HashIDMatchesThatTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.ticketWithBody(t, "Plain title", "")
	f.ticketWithBody(t, "Other", "")
	mentions := f.ticketWithBody(t, "Follow-up to "+fmt.Sprintf("#%d", target.ID), "")
	f.ticketWithBody(t, "Unrelated", "")

	ref := fmt.Sprintf("#%d", target.ID)
	assert.ElementsMatch(t, []int64{target.ID, mentions.ID}, f.searchText(t, ref))
	assert.Equal(t, []int64{target.ID}, f.searchText(t, ref+" plain"),
		"the id is one word among the others")
	assert.Empty(t, f.searchText(t, ref+" other"))
	for _, malformed := range []string{ref + "x", ref + "-1", "#" + fmt.Sprint(target.ID) + ".0"} {
		assert.Empty(t, f.searchText(t, malformed), "%q is text, not a reference", malformed)
	}
	huge := f.ticketWithBody(t, "Mentions #999999999999999999999", "")
	assert.Equal(t, []int64{huge.ID}, f.searchText(t, "#999999999999999999999"),
		"an id too large for int64 is just text")
	assert.Empty(t, f.search(t, &ticket.ListFilter{Text: ref, Statuses: []ticket.Status{ticket.StatusParked}}),
		"the status filter still applies")
}

// An agent's search is blind to a sibling attempt's comments and handoffs
// (a match would tell it what the sibling said), but still finds the
// ticket's own text and a single-attempt ticket's.
func TestListText_TextSkipsAttemptsHidesAttemptScopedText(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, _ := f.running(t)
	_, err := f.svc.Discuss(f.ctx, a1, "Which shape?", "handoff about the QUOKKA")
	require.NoError(t, err)
	f.personCommentsOn(t, id, 1, "attempt one quokka only")
	f.comment(t, id, "ticket wide wombat")

	agent := &ticket.ListFilter{TextSkipsAttempts: true, Text: "quokka"}
	assert.Empty(t, f.search(t, agent), "attempt-scoped text is not searchable by an agent")
	assert.Equal(t, []int64{id}, f.searchText(t, "quokka"), "the board still finds it")
	agent.Text = "wombat"
	assert.Equal(t, []int64{id}, f.search(t, agent), "the ticket's own comment is found")

	single := f.ticketWithBody(t, "Solo", "")
	f.comment(t, single.ID, "solo quokka")
	agent.Text = "quokka"
	assert.Equal(t, []int64{single.ID}, f.search(t, agent), "a single-attempt ticket has no sibling to hide")
}
