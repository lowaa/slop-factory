package scaleperf_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema/seeddata"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticketusage"
)

// pageSize is what a client asks for: the lists draw a screenful, never all.
const pageSize = 50

// world is a seeded tenant and the services the ticket lists are read through.
type world struct {
	ctx     context.Context
	sum     *seeddata.ScaleSummary
	tickets *ticket.Service
	pending *pendingaction.Service
	reports *reports.Service
	costs   *ticketusage.PostgresStore
}

func seeded(t *testing.T, sc seeddata.Scale) *world {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenant)
	sum, err := seeddata.SeedScale(ctx, database, tenant, sc)
	require.NoError(t, err)
	// Half of the tickets in a work column have a second attempt in the same
	// column, and a third still running: the seed writes the one attempt a
	// ticket had before there could be several, and the board's cards are
	// per attempt.
	_, err = database.Exec(ctx, `
		INSERT INTO attempts (ticket_id, number, status)
		SELECT t.id, n.number, CASE n.number WHEN 2 THEN t.status ELSE 'in_progress' END
		  FROM tickets t CROSS JOIN (VALUES (2), (3)) n(number)
		 WHERE t.status IN ('in_progress', 'discussion', 'review') AND t.id % 2 = 0`)
	require.NoError(t, err)
	_, err = database.Exec(ctx, `
		UPDATE tickets SET next_attempt_number = 4 WHERE status IN ('in_progress', 'discussion', 'review') AND id % 2 = 0`)
	require.NoError(t, err)
	return &world{
		costs:   ticketusage.NewPostgresStore(database),
		ctx:     ctx,
		sum:     sum,
		tickets: ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler),
		pending: pendingaction.NewService(pendingaction.NewPostgresStore(database), pendingaction.Config{}, slog.DiscardHandler),
		reports: reports.NewService(reports.NewPostgresStore(database, time.Minute), time.Now),
	}
}

// list is one of the lists a person opens. run reads it the way its page
// does today and says how many rows came back.
type list struct {
	name string
	run  func(w *world) (rows int, err error)
	// maxRows is the most a list may draw however many tickets there are, or
	// zero for a list that is not paged yet and so has no ceiling to hold it
	// to; its time budget still applies.
	maxRows int
}

func ticketPage(filter *ticket.ListFilter) func(w *world) (int, error) {
	return func(w *world) (int, error) {
		filter.Limit = pageSize
		page, err := w.tickets.ListPage(w.ctx, filter)
		if err != nil {
			return 0, err
		}
		return len(page.Tickets), nil
	}
}

// sortPages reads a sort's first page and one reached by following cursors,
// for every registered sort: the order is what an index serves, and a keyset
// page is meant to cost the same wherever it is.
func sortPages() []list {
	const deepPages = 5
	var out []list
	for _, name := range ticket.SortNames() {
		sort, err := ticket.SortByName(name)
		if err != nil {
			panic(err)
		}
		out = append(out, list{"sort " + name, ticketPage(&ticket.ListFilter{Sort: sort}), pageSize})
		out = append(out, list{fmt.Sprintf("sort %s, page %d", name, deepPages), func(w *world) (int, error) {
			filter := &ticket.ListFilter{Sort: sort, Limit: pageSize}
			var page *ticket.Page
			for range deepPages {
				var err error
				if page, err = w.tickets.ListPage(w.ctx, filter); err != nil {
					return 0, err
				}
				if page.Next == nil {
					return 0, fmt.Errorf("%s ran out of tickets before page %d", name, deepPages)
				}
				filter.After = page.Next
			}
			return len(page.Tickets), nil
		}, pageSize})
	}
	return out
}

// cardPages reads a work column as attempt cards, its first page and one
// reached by following cursors (as far as the column goes, which at the size
// the budget is held at is three pages), under every registered sort: the order of
// the tickets is an index and the attempts are joined to it, so a page
// costs the same wherever it is.
func cardPages(status ticket.Status) []list {
	const deepPages = 3
	var out []list
	for _, name := range ticket.SortNames() {
		sort, err := ticket.SortByName(name)
		if err != nil {
			panic(err)
		}
		filter := func() *ticket.ListFilter {
			return &ticket.ListFilter{Statuses: []ticket.Status{status}, AttemptCards: true, Sort: sort, Limit: pageSize, Summary: true}
		}
		out = append(out, list{fmt.Sprintf("%s cards, sort %s", status, name), func(w *world) (int, error) {
			page, err := w.tickets.ListPage(w.ctx, filter())
			if err != nil {
				return 0, err
			}
			return len(page.Tickets), nil
		}, pageSize})
		out = append(out, list{fmt.Sprintf("%s cards, sort %s, page %d", status, name, deepPages), func(w *world) (int, error) {
			f := filter()
			var page *ticket.Page
			for range deepPages {
				var err error
				if page, err = w.tickets.ListPage(w.ctx, f); err != nil {
					return 0, err
				}
				if page.Next == nil {
					break
				}
				// As a client sends it back, through the wire form.
				if f.After, err = ticket.DecodePageCursor(page.Next.Encode()); err != nil {
					return 0, err
				}
			}
			return len(page.Tickets), nil
		}, pageSize})
	}
	return out
}

// boardWithCosts is the board as the handler serves it: the columns, then the
// worker cost of every attempt card on them in one read.
func boardWithCosts(w *world) (int, error) {
	columns, err := w.tickets.ListBoard(w.ctx, &ticket.ListFilter{Limit: pageSize, Summary: true})
	if err != nil {
		return 0, err
	}
	rows := 0
	var refs []ticket.AttemptRef
	for _, c := range columns {
		rows += len(c.Page.Tickets)
		for i := range c.Page.Tickets {
			if tk := &c.Page.Tickets[i]; tk.Card.Multiple() {
				refs = append(refs, ticket.AttemptRef{TicketID: tk.ID, Number: tk.Card.Number})
			}
		}
	}
	if len(refs) == 0 {
		return 0, fmt.Errorf("the board has no card of several attempts to cost")
	}
	costs, err := w.costs.AttemptCosts(w.ctx, refs)
	if err != nil {
		return 0, err
	}
	if len(costs) == 0 {
		return 0, fmt.Errorf("no attempt card has a cost to read")
	}
	return rows, nil
}

// blockedPage reads the blocked list's first page, or its last by following
// cursors: every page ranks all the blocked tickets, so the last is the one
// that costs most.
func blockedPage(last bool) func(w *world) (int, error) {
	return func(w *world) (int, error) {
		filter := &ticket.ListFilter{Limit: pageSize}
		for {
			page, err := w.tickets.BlockedPage(w.ctx, filter)
			if err != nil {
				return 0, err
			}
			if !last || page.Next == nil {
				return len(page.Tickets), nil
			}
			filter.After = page.Next
		}
	}
}

// Every list is a page, or one per status for the board: the ceilings are the
// page size, not a function of how many tickets exist.
var lists = append([]list{
	{"board with done", func(w *world) (int, error) {
		columns, err := w.tickets.ListBoard(w.ctx, &ticket.ListFilter{Limit: pageSize})
		rows := 0
		for _, c := range columns {
			rows += len(c.Page.Tickets)
		}
		return rows, err
	}, len(model.Statuses()) * pageSize},
	{"board with attempt cards and their cost", boardWithCosts, len(model.Statuses()) * pageSize},
	{"blocked tickets", ticketPage(&ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusBlocked}}), pageSize},
	{"blocked list", blockedPage(false), pageSize},
	{"blocked list, last page", blockedPage(true), pageSize},
	{"blocking graph", func(w *world) (int, error) {
		graph, err := w.tickets.Graph(w.ctx, &ticket.GraphQuery{})
		if err != nil {
			return 0, err
		}
		return len(graph.Nodes), nil
	}, 0},
	{"unreviewed", ticketPage(&ticket.ListFilter{AwaitingPostReview: true}), pageSize},
	{"approvals", func(w *world) (int, error) {
		views, err := w.pending.Views(w.ctx, &pendingaction.ListFilter{States: []pendingaction.State{pendingaction.StatePending}})
		return len(views), err
	}, 0},
	{"reports by ticket", func(w *world) (int, error) {
		page, err := w.reports.LLMUsageTickets(w.ctx, 30, &reports.UsageTicketQuery{Filter: &ticket.ListFilter{}, Limit: 10})
		if err != nil {
			return 0, err
		}
		return len(page.Items), nil
	}, 10},
	{"text search, rare word", func(w *world) (int, error) {
		return ticketPage(&ticket.ListFilter{Text: w.sum.SearchWord})(w)
	}, pageSize},
	{"text search, common word", func(w *world) (int, error) {
		return ticketPage(&ticket.ListFilter{Text: w.sum.CommonWord})(w)
	}, pageSize},
}, append(append(sortPages(), cardPages(ticket.StatusReview)...), cardPages(ticket.StatusInProgress)...)...)
