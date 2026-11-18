package ticket

import (
	"context"
	"fmt"
	"slices"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// ListPage is List a filter.Limit tickets at a time (DefaultPageSize when
// zero, at most MaxPageSize), resuming after filter.After. It reads one
// ticket more than the page holds, to know whether there is a next page
// without a count.
func (s *Service) ListPage(ctx context.Context, filter *ListFilter) (*Page, error) {
	paged := *filter
	switch {
	case paged.Limit == 0:
		paged.Limit = DefaultPageSize
	case paged.Limit < 0 || paged.Limit > MaxPageSize:
		return nil, fmt.Errorf("%w: limit must be 1 to %d", ErrInvalidPage, MaxPageSize)
	}
	size := paged.Limit
	paged.Limit = size + 1
	tickets, err := s.List(ctx, &paged)
	if err != nil {
		return nil, err
	}
	page := &Page{Tickets: tickets}
	if len(tickets) > size {
		page.Tickets = tickets[:size]
		page.Next = newPageCursor(&paged, &page.Tickets[size-1])
	}
	return page, nil
}

// BlockedPage is ListPage over the tickets with an unfinished blocker, the
// one that would free the most first. filter.Sort is ignored: the order is
// fixed, and a cursor is bound to it.
func (s *Service) BlockedPage(ctx context.Context, filter *ListFilter) (*Page, error) {
	blocked := *filter
	blocked.Blocked, blocked.Sort, blocked.Summary = true, blockingSort, true
	return s.ListPage(ctx, &blocked)
}

// BoardColumn is one status of the board: its first page and the count of
// every card in it. Next resumes the column with ListPage under the same
// filter and Statuses of just this status.
type BoardColumn struct {
	Status Status
	Count  int
	Page   *Page
}

// ListBoard returns a column per status in filter.Statuses (every status
// when empty), each its first filter.Limit tickets. A column's cursor is
// issued for the filter narrowed to its status, which is the query a client
// repeats to load more. Counts and pages are separate reads, so a ticket
// moving between them can leave a count off by one until the next load.
func (s *Service) ListBoard(ctx context.Context, filter *ListFilter) ([]BoardColumn, error) {
	statuses := model.Statuses()
	if len(filter.Statuses) > 0 {
		// One column per status, however often a client names it.
		statuses = nil
		for _, st := range filter.Statuses {
			if !st.Valid() {
				return nil, fmt.Errorf("%w: status %q", ErrInvalidEnum, st)
			}
			if !slices.Contains(statuses, st) {
				statuses = append(statuses, st)
			}
		}
	}
	if filter.After != nil {
		return nil, fmt.Errorf("%w: the board has no cursor, page a column", ErrInvalidPage)
	}
	counts, err := s.boardCounts(ctx, filter, statuses)
	if err != nil {
		return nil, err
	}
	columns := make([]BoardColumn, 0, len(statuses))
	for _, st := range statuses {
		column := *filter
		column.Statuses = []Status{st}
		column.AttemptCards = st.IsWork()
		page, err := s.ListPage(ctx, &column)
		if err != nil {
			return nil, err
		}
		columns = append(columns, BoardColumn{Status: st, Count: counts[st], Page: page})
	}
	return columns, nil
}

// boardCounts counts the cards of each of statuses: attempts for the work
// columns and tickets for the rest, one read for each kind.
func (s *Service) boardCounts(ctx context.Context, filter *ListFilter, statuses []Status) (map[Status]int, error) {
	var work, other []Status
	for _, st := range statuses {
		if st.IsWork() {
			work = append(work, st)
		} else {
			other = append(other, st)
		}
	}
	counts := map[Status]int{}
	for _, kind := range []struct {
		statuses []Status
		cards    bool
	}{{other, false}, {work, true}} {
		if len(kind.statuses) == 0 {
			continue
		}
		counted := *filter
		counted.Statuses, counted.AttemptCards = kind.statuses, kind.cards
		got, err := s.store.CountTickets(ctx, &counted)
		if err != nil {
			return nil, err
		}
		for st, n := range got {
			counts[st] = n
		}
	}
	return counts, nil
}
