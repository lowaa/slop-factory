package ticket_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticketusage"
)

// The board's work columns list attempts, in their own attempts' statuses.
// Against real Postgres: the query is the thing under test.

// withAttempts files a ticket and gives it one attempt per status, attempt 1
// (promoted) in the first. The ticket's status is what its attempts derive to,
// as the service keeps it.
func (f *fixture) withAttempts(t *testing.T, title string, statuses ...string) *ticket.Ticket {
	t.Helper()
	tk := f.ticket(t, title)
	for i := 1; i < len(statuses); i++ {
		require.Equal(t, i+1, f.addAttempt(t, tk.ID, false))
	}
	for i, st := range statuses {
		f.run(t, `UPDATE attempts SET status = $3 WHERE ticket_id = $1 AND number = $2`, tk.ID, i+1, st)
	}
	f.run(t, `UPDATE tickets SET status = $2 WHERE id = $1`, tk.ID, derivedStatusOf(statuses))
	return tk
}

// derivedStatusOf is the ticket status the service gives a set of attempt
// statuses: a person's discussion and review first.
func derivedStatusOf(statuses []string) string {
	for _, st := range []string{"discussion", "review", "in_progress"} {
		if slices.Contains(statuses, st) {
			return st
		}
	}
	return "open"
}

type cardKey struct {
	Ticket  int64
	Attempt int
}

// cardsOf names the cards of a page; a ticket card has attempt 0.
func cardsOf(tickets []ticket.Ticket) []cardKey {
	out := []cardKey{}
	for _, tk := range tickets {
		k := cardKey{Ticket: tk.ID}
		if tk.Card != nil {
			k.Attempt = tk.Card.Number
		}
		out = append(out, k)
	}
	return out
}

func (f *fixture) column(t *testing.T, filter *ticket.ListFilter, status ticket.Status) ticket.BoardColumn {
	t.Helper()
	columns, err := f.svc.ListBoard(f.ctx, filter)
	require.NoError(t, err)
	for _, c := range columns {
		if c.Status == status {
			return c
		}
	}
	require.FailNow(t, "no such column", string(status))
	return ticket.BoardColumn{}
}

func TestBoard_AnAttemptIsACardInItsOwnColumn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	both := f.withAttempts(t, "Two attempts", "review", "in_progress")
	solo := f.withAttempts(t, "One attempt", "in_progress")
	waiting := f.ticket(t, "Waiting")
	closedOut := f.withAttempts(t, "A loser and a runner", "closed", "discussion")
	filter := &ticket.ListFilter{Limit: 50, Summary: true}

	review := f.column(t, filter, ticket.StatusReview)
	progress := f.column(t, filter, ticket.StatusInProgress)
	discussion := f.column(t, filter, ticket.StatusDiscussion)
	open := f.column(t, filter, ticket.StatusOpen)

	assert.Equal(t, []cardKey{{both.ID, 1}}, cardsOf(review.Page.Tickets), "attempt 1 is in review, where its ticket is too")
	assert.Equal(t, []cardKey{{solo.ID, 1}, {both.ID, 2}}, cardsOf(progress.Page.Tickets),
		"attempt 2 is in progress though its ticket is in review")
	assert.Equal(t, []cardKey{{closedOut.ID, 2}}, cardsOf(discussion.Page.Tickets), "a closed attempt is no card")
	assert.Equal(t, []cardKey{{waiting.ID, 0}}, cardsOf(open.Page.Tickets), "the other columns list tickets")
	assert.Equal(t, 1, review.Count)
	assert.Equal(t, 2, progress.Count, "a column's count is of cards")
	assert.Equal(t, 1, discussion.Count)
	assert.Equal(t, 1, open.Count)

	card := progress.Page.Tickets[1]
	assert.Equal(t, both.Title, card.Title)
	assert.Equal(t, ticket.StatusReview, card.Status, "the ticket's own status is untouched")
	body := ticketBodyOf(card)
	assert.Equal(t, ticket.Status("in_progress"), body.Status)
	assert.Empty(t, body.AllowedMoves, "a status move is the ticket's, not a card's")
	assert.False(t, body.CanReady)
	assert.Equal(t, 2, card.Card.Total)
	assert.Equal(t, ticket.Status("in_progress"), card.Card.Status)
	assert.False(t, card.Card.AwaitingChoice)
	assert.Nil(t, card.Card.Variant)
}

func TestBoard_AFinishedUnpromotedAttemptAwaitsChoice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.withAttempts(t, "Raced", "in_progress", "review", "review")
	f.run(t, `UPDATE attempts SET promoted_at = NULL, promoted_by_user_id = NULL WHERE ticket_id = $1 AND number = 1`, tk.ID)
	f.run(t, `UPDATE attempts SET promoted_at = now() WHERE ticket_id = $1 AND number = 3`, tk.ID)
	version := f.namedVariantVersion(t, "careful", 3)
	f.run(t, `UPDATE attempts SET variant_version_id = $2 WHERE ticket_id = $1 AND number = 2`, tk.ID, version)
	f.archiveVariant(t, version)
	f.run(t, `UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/2' WHERE ticket_id = $1 AND number = 2`, tk.ID)

	review := f.column(t, &ticket.ListFilter{Limit: 50, Summary: true}, ticket.StatusReview)

	require.Equal(t, []cardKey{{tk.ID, 3}, {tk.ID, 2}}, cardsOf(review.Page.Tickets), "newest first, a ticket's attempts too")
	promoted, awaiting := review.Page.Tickets[0], review.Page.Tickets[1]
	assert.False(t, promoted.Card.AwaitingChoice, "a promoted attempt in review is waiting on a review, not a choice")
	assert.True(t, awaiting.Card.AwaitingChoice)
	require.NotNil(t, awaiting.Card.Variant)
	assert.Equal(t, "careful", awaiting.Card.Variant.Name)
	assert.Equal(t, 3, awaiting.Card.Variant.Version)
	assert.True(t, awaiting.Card.Variant.Archived)
	require.NotNil(t, awaiting.PRURL)
	assert.Equal(t, "https://github.com/o/r/pull/2", *awaiting.PRURL, "the card's PR link is its attempt's")
	assert.Nil(t, promoted.PRURL)
}

// A ticket with one attempt is the card it always was: on the wire nothing of
// the attempt shows, so the contract of the board is unchanged for it.
func TestBoard_ATicketWithOneAttemptIsTheTicketCardAsBefore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	solo := f.withAttempts(t, "One attempt", "review")
	multi := f.withAttempts(t, "Several", "review", "review")

	rec := send(t, f.boardMux(), http.MethodGet, "/api/board?status=review", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var board struct {
		Columns []struct {
			Count   int                          `json:"count"`
			Tickets []map[string]json.RawMessage `json:"tickets"`
		} `json:"columns"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &board))
	require.Len(t, board.Columns, 1)
	assert.Equal(t, 3, board.Columns[0].Count)
	byAttempt := map[string]map[string]json.RawMessage{}
	for _, tk := range board.Columns[0].Tickets {
		var id int64
		require.NoError(t, json.Unmarshal(tk["id"], &id))
		_, has := tk["attempt"]
		byAttempt[fmt.Sprintf("%d/%t", id, has)] = tk
	}
	assert.Contains(t, byAttempt, fmt.Sprintf("%d/false", solo.ID), "no attempt block on a one-attempt ticket's card")
	assert.Contains(t, byAttempt, fmt.Sprintf("%d/true", multi.ID))
	assert.Len(t, board.Columns[0].Tickets, 3)
}

func TestBoard_ACardCarriesItsAttemptOnTheWire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.withAttempts(t, "Two", "in_progress", "review")
	f.run(t, `UPDATE attempts SET promoted_at = NULL, promoted_by_user_id = NULL WHERE ticket_id = $1 AND number = 2`, tk.ID)

	rec := send(t, f.boardMux(), http.MethodGet, "/api/board?status=review", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var board ticket.BoardResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &board.Body))
	require.Len(t, board.Body.Columns, 1)
	require.Len(t, board.Body.Columns[0].Tickets, 1)
	card := board.Body.Columns[0].Tickets[0]
	require.NotNil(t, card.Attempt)
	assert.Equal(t, "review", string(card.Status), "the card's status is its attempt's")
	assert.Equal(t, ticket.AttemptCardBody{Number: 2, Total: 2, Status: "review", TicketStatus: ticket.StatusReview,
		AwaitingChoice: true, WorkerCost: []ticket.CostFigureBody{}}, *card.Attempt)
}

// Every sort walks the same cards in the same order at every page size, with
// the boundary falling between attempts of one ticket, and a cursor carries
// the column to the later pages, through the HTTP route as a client uses it.
func TestAttemptCards_APageBoundaryBetweenAttemptsNeitherSkipsNorRepeats(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for i := 0; i < 4; i++ {
		f.withAttempts(t, fmt.Sprintf("Three %d", i), "in_progress", "in_progress", "review", "in_progress")
		f.withAttempts(t, fmt.Sprintf("Solo %d", i), "in_progress")
	}
	// Ties on every sort's first key: same priority and update time.
	f.run(t, `UPDATE tickets SET priority = 'normal', updated_at = '2024-01-01T00:00:00Z' WHERE id % 2 = 0`)
	// Some closed_at values and a NULL tail, so recently_closed reads a page
	// as two branches and merges them (see afterBranches).
	f.run(t, `UPDATE tickets SET closed_at = '2024-02-01T00:00:00Z' WHERE id % 3 = 0`)

	for _, name := range ticket.SortNames() {
		t.Run(name, func(t *testing.T) {
			sort, err := ticket.SortByName(name)
			require.NoError(t, err)
			base := ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusInProgress}, AttemptCards: true, Sort: sort, Summary: true}
			all := base
			all.Limit = ticket.MaxPageSize
			whole, err := f.svc.ListPage(f.ctx, &all)
			require.NoError(t, err)
			want := cardsOf(whole.Tickets)
			require.Len(t, want, 4*3+4, "every in-progress attempt, once")
			require.Len(t, slices.Compact(slices.Clone(want)), len(want))

			for size := 1; size <= len(want)+1; size++ {
				filter := base
				filter.Limit = size
				var got []cardKey
				for {
					page, err := f.svc.ListPage(f.ctx, &filter)
					require.NoError(t, err)
					got = append(got, cardsOf(page.Tickets)...)
					if page.Next == nil {
						break
					}
					assert.True(t, page.Next.Cards)
					filter.After, err = ticket.DecodePageCursor(page.Next.Encode())
					require.NoError(t, err)
				}
				assert.Equal(t, want, got, "page size %d", size)
			}
		})
	}
}

// recently_closed reads a page past its first as branches merged by an outer
// order, which Postgres may break ties in any way it likes; so the merge must
// name the attempt number as well, which a walk's luck would not show.
func TestAttemptCards_TheMergeOfBranchesOrdersByTheAttemptToo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		f.withAttempts(t, "Three", "in_progress", "in_progress", "in_progress")
	}
	f.run(t, `UPDATE tickets SET closed_at = now()`)
	sort, err := ticket.SortByName(ticket.SortRecentlyClosed)
	require.NoError(t, err)
	filter := ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusInProgress}, AttemptCards: true, Sort: sort, Limit: 2}
	page, err := f.svc.ListPage(f.ctx, &filter)
	require.NoError(t, err)
	filter.After = page.Next

	query, _, err := ticket.ListQuery(&filter)

	require.NoError(t, err)
	assert.Contains(t, query, ") u ORDER BY u.closed_at DESC NULLS LAST, u.id DESC, u.attempt_number")
}

func TestAttemptCards_LaterPagesComeFromTheTicketsRouteByCursor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.withAttempts(t, "Many", "in_progress", "in_progress", "in_progress")
	other := f.withAttempts(t, "Other", "in_progress", "in_progress")
	mux := f.boardMux()

	var first struct {
		Columns []struct {
			Status     string `json:"status"`
			Count      int    `json:"count"`
			NextCursor string `json:"next_cursor"`
			Tickets    []struct {
				ID      int64 `json:"id"`
				Attempt *struct {
					Number int `json:"number"`
				} `json:"attempt"`
			} `json:"tickets"`
		} `json:"columns"`
	}
	rec := send(t, mux, http.MethodGet, "/api/board?status=in_progress&limit=2", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &first))
	col := first.Columns[0]
	require.Equal(t, 5, col.Count)
	require.NotEmpty(t, col.NextCursor)
	var got []cardKey
	for _, c := range col.Tickets {
		got = append(got, cardKey{c.ID, c.Attempt.Number})
	}
	cursor := col.NextCursor
	for cursor != "" {
		rec := send(t, mux, http.MethodGet, "/api/tickets?status=in_progress&limit=2&cursor="+cursor, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var page struct {
			Tickets []struct {
				ID      int64 `json:"id"`
				Attempt *struct {
					Number int `json:"number"`
				} `json:"attempt"`
			} `json:"tickets"`
			NextCursor string `json:"next_cursor"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		for _, c := range page.Tickets {
			got = append(got, cardKey{c.ID, c.Attempt.Number})
		}
		cursor = page.NextCursor
	}
	assert.Equal(t, []cardKey{{other.ID, 2}, {other.ID, 1}, {tk.ID, 3}, {tk.ID, 2}, {tk.ID, 1}}, got)
}

func TestAttemptCards_ACursorBelongsToItsKindOfList(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.withAttempts(t, "Many", "in_progress", "in_progress", "in_progress")
	cards := ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusInProgress}, AttemptCards: true, Limit: 1}
	page, err := f.svc.ListPage(f.ctx, &cards)
	require.NoError(t, err)
	tickets := ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusInProgress}, Limit: 1}
	plain, err := f.svc.ListPage(f.ctx, &tickets)
	require.NoError(t, err)
	_ = plain

	asTickets := tickets
	asTickets.After = page.Next
	_, err = f.svc.ListPage(f.ctx, &asTickets)
	assert.ErrorIs(t, err, ticket.ErrInvalidPage, "a card cursor does not page tickets")

	f.withAttempts(t, "Another", "in_progress")
	plain, err = f.svc.ListPage(f.ctx, &ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusInProgress}, Limit: 1})
	require.NoError(t, err)
	require.NotNil(t, plain.Next)
	asCards := cards
	asCards.After = plain.Next
	_, err = f.svc.ListPage(f.ctx, &asCards)
	assert.ErrorIs(t, err, ticket.ErrInvalidPage, "nor a ticket cursor, cards")
}

func TestAttemptCards_OnlyTheWorkStatusesAreListedAsAttempts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusOpen}, AttemptCards: true})
	assert.ErrorIs(t, err, ticket.ErrInvalidPage)
}

// A ticket moving between statuses is one ticket's attempts moving between
// columns, and the cards follow the attempts and not the ticket.
func TestBoard_CardsFollowTheirAttemptsAsTheyMove(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.withAttempts(t, "Moving", "in_progress", "in_progress")
	filter := &ticket.ListFilter{Limit: 50, Summary: true}
	require.Equal(t, 2, f.column(t, filter, ticket.StatusInProgress).Count)

	f.run(t, `UPDATE attempts SET status = 'discussion' WHERE ticket_id = $1 AND number = 2`, tk.ID)
	f.run(t, `UPDATE tickets SET status = 'discussion' WHERE id = $1`, tk.ID)

	assert.Equal(t, []cardKey{{tk.ID, 1}}, cardsOf(f.column(t, filter, ticket.StatusInProgress).Page.Tickets))
	assert.Equal(t, []cardKey{{tk.ID, 2}}, cardsOf(f.column(t, filter, ticket.StatusDiscussion).Page.Tickets))
}

// -- cost --

// countingCosts is the real aggregate, counting its calls.
type countingCosts struct {
	inner ticket.AttemptCosts
	calls atomic.Int64
	refs  atomic.Int64
}

func (c *countingCosts) AttemptCosts(ctx context.Context, refs []ticket.AttemptRef) (map[ticket.AttemptRef][]ticket.CostFigure, error) {
	c.calls.Add(1)
	c.refs.Add(int64(len(refs)))
	return c.inner.AttemptCosts(ctx, refs)
}

func (f *fixture) costMux(costs ticket.AttemptCosts) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, _ string) (*authn.User, error) {
		return &authn.User{ID: f.human, TenantID: f.tenant}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, ticket.NewHandler(f.svc, slog.DiscardHandler, ticket.WithAttemptCosts(costs)))
	return mux
}

func (f *fixture) usage(t *testing.T, ticketID int64, attempt int, key string, requests, input int64, estimated int64) {
	t.Helper()
	f.usageIn(t, ticketID, attempt, key, "metered", requests, input, &estimated)
}

// usageIn reports usage in a billing mode; no estimate is an unpriced record.
func (f *fixture) usageIn(t *testing.T, ticketID int64, attempt int, key, mode string, requests, input int64, estimated *int64) {
	t.Helper()
	agentID := f.agent(t, "worker-"+key)
	f.run(t, `
		INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope, ticket_id, attempt_number,
		    provider, model, billing_mode, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		    reasoning_tokens, estimated_cost_microdollars, price_version, unpriced, started_at, ended_at)
		VALUES ($1, $2, 'pi', 's', 'main', 'ticket', $3, $4, 'p', 'm', $8, $5, $6, 0, 0, 0, 0, $7, 'v', $7::bigint IS NULL, now(), now())`,
		agentID, key, ticketID, attempt, requests, input, estimated, mode)
}

func TestBoard_ACardsWorkerCostIsItsAttemptsAndOneReadFetchesThemAll(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.withAttempts(t, "A", "review", "in_progress")
	b := f.withAttempts(t, "B", "in_progress", "discussion", "review")
	solo := f.withAttempts(t, "Solo", "in_progress")
	f.usage(t, a.ID, 1, "a1x", 2, 100, 10)
	f.usage(t, a.ID, 1, "a1y", 1, 50, 5)
	f.usage(t, a.ID, 2, "a2", 4, 400, 40)
	f.usage(t, b.ID, 3, "b3", 1, 1, 1)
	f.usage(t, solo.ID, 1, "s1", 9, 9, 9)
	f.usageIn(t, b.ID, 1, "b1", "subscription", 5, 50, nil)
	counted := &countingCosts{inner: ticketusage.NewPostgresStore(f.database)}
	mux := f.costMux(counted)

	rec := send(t, mux, http.MethodGet, "/api/board", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var board ticket.BoardResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &board.Body))

	costOf := map[cardKey]*ticket.AttemptCardBody{}
	for _, c := range board.Body.Columns {
		for _, tk := range c.Tickets {
			if tk.Attempt != nil {
				costOf[cardKey{tk.ID, tk.Attempt.Number}] = tk.Attempt
			}
		}
	}
	require.Len(t, costOf, 5, "the one-attempt ticket's card has no attempt block, so no cost")
	one := func(k cardKey) ticket.CostFigureBody {
		require.Len(t, costOf[k].WorkerCost, 1, "%v", k)
		return costOf[k].WorkerCost[0]
	}
	assert.EqualValues(t, 15, *one(cardKey{a.ID, 1}).CostMicros, "attempt 1's two transcripts")
	assert.EqualValues(t, 150, one(cardKey{a.ID, 1}).InputTokens)
	assert.EqualValues(t, 40, *one(cardKey{a.ID, 2}).CostMicros, "and attempt 2's alone")
	assert.EqualValues(t, 1, *one(cardKey{b.ID, 3}).CostMicros)
	assert.Empty(t, costOf[cardKey{b.ID, 2}].WorkerCost, "an attempt that spent nothing")
	unpriced := one(cardKey{b.ID, 1})
	assert.Equal(t, "subscription", unpriced.BillingMode)
	assert.Nil(t, unpriced.CostMicros, "no priced record is not a zero")
	assert.EqualValues(t, 1, unpriced.UnpricedRecords)
	assert.EqualValues(t, 50, unpriced.InputTokens)
	assert.EqualValues(t, 1, counted.calls.Load(), "one aggregate for the whole board")
	assert.EqualValues(t, 5, counted.refs.Load())

	counted.calls.Store(0)
	rec = send(t, mux, http.MethodGet, "/api/board?status=review&limit=1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var page struct {
		Columns []struct {
			NextCursor string `json:"next_cursor"`
		} `json:"columns"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Equal(t, int64(1), counted.calls.Load())
	counted.calls.Store(0)
	rec = send(t, mux, http.MethodGet, "/api/tickets?status=review&limit=50&cursor="+page.Columns[0].NextCursor, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.EqualValues(t, 1, counted.calls.Load(), "a later page is one read too")
}

func TestBoard_ABoardWithNoSeveralAttemptTicketsReadsNoCost(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.withAttempts(t, "Solo", "in_progress")
	counted := &countingCosts{inner: ticketusage.NewPostgresStore(f.database)}

	rec := send(t, f.costMux(counted), http.MethodGet, "/api/board", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.EqualValues(t, 0, counted.calls.Load())
}

// -- the stream --

// An attempt changing moves the board revision, and the delta names its
// ticket and carries the ticket's cards, so the client redraws every one.
func TestBoardDelta_AnAttemptChangeRedrawsTheTicketsCards(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.withAttempts(t, "Two", "in_progress", "in_progress")
	f.ticket(t, "Bystander")
	before := f.boardRevision(t)

	f.run(t, `UPDATE attempts SET status = 'review' WHERE ticket_id = $1 AND number = 2`, tk.ID)

	assert.Greater(t, f.boardRevision(t), before, "a write to an attempt alone moves the revision")
	delta := f.boardDelta(t, before)
	assert.False(t, delta.Reload)
	assert.Equal(t, []int64{tk.ID}, deltaIDs(delta), "keyed by ticket")
	assert.Equal(t, []cardKey{{tk.ID, 2}, {tk.ID, 1}}, cardsOf(delta.Cards), "all the ticket's cards, not just the changed attempt's")
	assert.Equal(t, ticket.Status("review"), delta.Cards[0].Card.Status)
}

// A ticket whose attempts have all left the work columns is drawn as the
// ticket it is, in its own column: the delta names it and has no cards.
func TestBoardDelta_ATicketWithNoLiveAttemptHasNoCards(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.withAttempts(t, "Settled", "in_progress", "in_progress")
	before := f.boardRevision(t)
	f.run(t, `UPDATE attempts SET status = CASE number WHEN 1 THEN 'done' ELSE 'closed' END WHERE ticket_id = $1`, tk.ID)
	f.run(t, `UPDATE tickets SET status = 'done' WHERE id = $1`, tk.ID)

	delta := f.boardDelta(t, before)

	assert.Equal(t, []int64{tk.ID}, deltaIDs(delta))
	assert.Equal(t, ticket.StatusDone, delta.Tickets[0].Status)
	assert.Empty(t, delta.Cards)
}

func TestBoardDelta_ATicketWithOneAttemptHasNoCards(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.withAttempts(t, "Solo", "in_progress")
	before := f.boardRevision(t)
	f.run(t, `UPDATE attempts SET status = 'review' WHERE ticket_id = $1`, tk.ID)

	delta := f.boardDelta(t, before)

	assert.Equal(t, []int64{tk.ID}, deltaIDs(delta))
	assert.Empty(t, delta.Cards, "its ticket card redraws as it did")
}

// fixedCosts answers what it was given.
type fixedCosts struct {
	figures map[ticket.AttemptRef][]ticket.CostFigure
	asked   [][]ticket.AttemptRef
}

func (c *fixedCosts) AttemptCosts(_ context.Context, refs []ticket.AttemptRef) (map[ticket.AttemptRef][]ticket.CostFigure, error) {
	c.asked = append(c.asked, refs)
	return c.figures, nil
}

func TestBoardStream_SendsTheChangedTicketsCardsWithTheirCost(t *testing.T) {
	t.Parallel()
	revisions := newFakeRevisions(7)
	micros := int64(1234)
	card := func(n int, st ticket.Status) ticket.Ticket {
		return ticket.Ticket{ID: 12, Title: "Split", Status: ticket.StatusReview, Priority: model.PriorityNormal,
			Card: &model.AttemptCard{Number: n, Total: 2, Status: st}}
	}
	revisions.setDelta(&ticket.BoardDelta{
		Tickets: []ticket.Ticket{{ID: 12, Title: "Split", Status: ticket.StatusReview, Priority: model.PriorityNormal}},
		Cards:   []ticket.Ticket{card(1, ticket.StatusReview), card(2, ticket.StatusInProgress)},
	}, nil)
	costs := &fixedCosts{figures: map[ticket.AttemptRef][]ticket.CostFigure{
		{TicketID: 12, Number: 1}: {{BillingMode: "metered", Requests: 9, CostMicros: &micros}},
		{TicketID: 12, Number: 2}: {{BillingMode: "metered", Requests: 3, CostMicros: &micros}},
	}}
	stream := openBoardStream(t, boardStreamServer(t, revisions, &ticket.BoardStreamOptions{Costs: costs}), uuid.New(), "?since=7", nil)
	stream.noEventYet(t)

	revisions.set(8)
	ev := stream.nextData(t)

	var body ticket.BoardChangeBody
	require.NoError(t, json.Unmarshal([]byte(ev.data), &body))
	require.Len(t, body.Tickets, 1)
	require.Len(t, body.Cards, 2)
	assert.Equal(t, []int{1, 2}, []int{body.Cards[0].Attempt.Number, body.Cards[1].Attempt.Number})
	assert.Equal(t, "in_progress", string(body.Cards[1].Status), "a card is in its attempt's column")
	require.Len(t, body.Cards[0].Attempt.WorkerCost, 1)
	assert.EqualValues(t, 9, body.Cards[0].Attempt.WorkerCost[0].Requests, "each card its own attempt's figure")
	require.Len(t, body.Cards[1].Attempt.WorkerCost, 1)
	assert.EqualValues(t, 3, body.Cards[1].Attempt.WorkerCost[0].Requests)
	assert.EqualValues(t, 1234, *body.Cards[1].Attempt.WorkerCost[0].CostMicros)
	assert.Equal(t, [][]ticket.AttemptRef{{{TicketID: 12, Number: 1}, {TicketID: 12, Number: 2}}}, costs.asked,
		"one read, for exactly the cards sent")
}

// ticketBodyOf is the body a card is served as.
func ticketBodyOf(tk ticket.Ticket) ticket.TicketBody { return ticket.TicketToBody(tk) }

// A filter by PR link on a list of cards is on the card's attempt, which is
// the link the card shows, and not on the ticket's promoted one.
func TestAttemptCards_APRLinkFilterIsOnTheCardsAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.withAttempts(t, "Two", "in_progress", "in_progress")
	f.run(t, `UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/2' WHERE ticket_id = $1 AND number = 2`, tk.ID)

	page, err := f.svc.ListPage(f.ctx, &ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusInProgress}, AttemptCards: true,
		PRLink: "https://github.com/o/r/pull/2", Limit: 50})

	require.NoError(t, err)
	assert.Equal(t, []cardKey{{tk.ID, 2}}, cardsOf(page.Tickets), "only the attempt with the link, though attempt 1 is the promoted one")
}

func TestGetTicket_EachAttemptCarriesItsOwnWorkerCostAsItsCardShowsIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.withAttempts(t, "A", "review", "in_progress", "in_progress")
	f.usage(t, tk.ID, 1, "a1x", 2, 100, 10)
	f.usage(t, tk.ID, 1, "a1y", 1, 50, 5)
	f.usage(t, tk.ID, 2, "a2", 4, 400, 40)
	f.usageIn(t, tk.ID, 3, "a3", "subscription", 5, 50, nil)
	f.usageIn(t, tk.ID, 2, "a2sub", "subscription", 7, 70, nil)
	mux := f.costMux(ticketusage.NewPostgresStore(f.database))

	get := func(path string) ticket.TicketBody {
		rec := send(t, mux, http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body ticket.TicketBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body
	}
	detail := get(fmt.Sprintf("/api/tickets/%d", tk.ID))
	require.Len(t, detail.Attempts, 3)
	one := func(n int) ticket.CostFigureBody {
		require.Len(t, detail.Attempts[n-1].WorkerCost, 1, "attempt %d", n)
		return detail.Attempts[n-1].WorkerCost[0]
	}
	require.Len(t, detail.Attempts[1].WorkerCost, 2, "attempt 2 spent in two billing modes")
	modes := map[string]int64{}
	for _, c := range detail.Attempts[1].WorkerCost {
		modes[c.BillingMode] = c.InputTokens
	}
	assert.Equal(t, map[string]int64{"metered": 400, "subscription": 70}, modes)
	assert.EqualValues(t, 15, *one(1).CostMicros, "attempt 1's two transcripts, not the ticket's total")
	assert.EqualValues(t, 150, one(1).InputTokens)
	assert.Equal(t, "subscription", one(3).BillingMode)
	assert.Nil(t, one(3).CostMicros, "no priced record is not a zero")

	var board ticket.BoardResponse
	rec := send(t, mux, http.MethodGet, "/api/board", "")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &board.Body))
	seen := 0
	for _, c := range board.Body.Columns {
		for _, card := range c.Tickets {
			if card.ID == tk.ID && card.Attempt != nil {
				seen++
				assert.Equal(t, detail.Attempts[card.Attempt.Number-1].WorkerCost, card.Attempt.WorkerCost, "attempt %d", card.Attempt.Number)
			}
		}
	}
	assert.Equal(t, 3, seen)
}

func TestGetTicket_AnAttemptThatSpentNothingHasAnEmptyWorkerCost(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.withAttempts(t, "A", "review", "in_progress")
	f.usage(t, tk.ID, 1, "a1", 1, 1, 1)
	mux := f.costMux(ticketusage.NewPostgresStore(f.database))
	rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d", tk.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"worker_cost":[]`)
}

type failingCosts struct{ err error }

func (c failingCosts) AttemptCosts(context.Context, []ticket.AttemptRef) (map[ticket.AttemptRef][]ticket.CostFigure, error) {
	return nil, c.err
}

func TestGetTicket_AFailedCostReadFailsTheRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.withAttempts(t, "A", "review", "in_progress")
	mux := f.costMux(failingCosts{err: errors.New("usage store down")})
	rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d", tk.ID), "")
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), `"attempts"`)
}
