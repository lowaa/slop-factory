package pendingaction

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// PageCursor is the position of the last proposal of a page. Clients only see
// it encoded, as an opaque string.
type PageCursor struct {
	// Query pins the cursor to the list it was issued for: replayed against
	// another filter or order it would skip or repeat proposals.
	Query     string    `json:"q"`
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"i"`
}

func (c *PageCursor) Encode() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodePageCursor(s string) (*PageCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: bad cursor", ticket.ErrInvalidPage)
	}
	var c PageCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.Query == "" || c.CreatedAt.IsZero() || c.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: bad cursor", ticket.ErrInvalidPage)
	}
	return &c, nil
}

// queryHash names the list a cursor belongs to: everything that selects or
// orders the proposals, but not the page size, so a client may change that
// mid-walk.
func (f *ListFilter) queryHash() string {
	states := make([]string, len(f.States))
	for i, st := range f.States {
		states[i] = string(st)
	}
	var tickets string
	if f.Ticket != nil {
		tickets = f.Ticket.QueryHash()
	}
	canonical, _ := json.Marshal([]any{f.AgentID, f.TicketID, f.Kind, states, f.IDs, tickets, f.Oldest})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:8])
}

// ViewPage is one page of proposals; Next is nil on the last.
type ViewPage struct {
	Views []View
	Next  *PageCursor
}

// ViewsPage is Views filter.Limit proposals at a time (DefaultPageSize when
// zero, at most MaxPageSize), resuming after filter.After. It reads one
// proposal more than the page holds, to know whether there is a next page
// without a count.
//
// As with the ticket lists, a walk is a keyset walk, not a snapshot: a
// proposal that enters the selected state behind the cursor (for a state
// other than pending, one decided mid-walk) is not met by it. Pending, which
// the Approvals page walks, is only entered by being created, newest last in
// the oldest-first order, so nothing is skipped there.
func (s *Service) ViewsPage(ctx context.Context, who *authn.User, filter *ListFilter) (*ViewPage, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	paged := *filter
	switch {
	case paged.Limit == 0:
		paged.Limit = DefaultPageSize
	case paged.Limit < 0 || paged.Limit > MaxPageSize:
		return nil, fmt.Errorf("%w: limit must be 1 to %d", ticket.ErrInvalidPage, MaxPageSize)
	}
	hash := paged.queryHash()
	if paged.After != nil && paged.After.Query != hash {
		return nil, fmt.Errorf("%w: the cursor belongs to another list", ticket.ErrInvalidPage)
	}
	size := paged.Limit
	paged.Limit = size + 1
	actions, err := s.List(ctx, &paged)
	if err != nil {
		return nil, err
	}
	page := &ViewPage{}
	if len(actions) > size {
		actions = actions[:size]
		last := &actions[size-1]
		page.Next = &PageCursor{Query: hash, CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if page.Views, err = s.describe(ctx, actions); err != nil {
		return nil, err
	}
	return page, nil
}
