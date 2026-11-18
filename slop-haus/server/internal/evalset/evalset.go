// Package evalset is the tenant's eval sets: named groups of benchmark
// tickets that a variant is replayed across as one replay batch.
package evalset

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const (
	MaxNameLen        = 128
	MaxDescriptionLen = 4000
	// MaxTickets bounds a set, so resolving a filter or a pinned list never
	// reads an unbounded number of tickets. It is well over any replay
	// batch's own maximum.
	MaxTickets = 500
)

var (
	ErrInvalid  = errors.New("evalset: invalid")
	ErrNotFound = errors.New("evalset: no such eval set")
	// ErrNameTaken is a name another set of the tenant already has.
	ErrNameTaken = errors.New("evalset: an eval set of that name already exists")
	// ErrChanged is a set edited by someone else while the caller worked from
	// an earlier read of it.
	ErrChanged = errors.New("evalset: the eval set changed meanwhile; try again")
	// ErrEmpty is a set with no tickets to run across.
	ErrEmpty = errors.New("evalset: the eval set has no tickets")
	// ErrReplaysDisabled is a run on a server that does not serve replays.
	ErrReplaysDisabled = errors.New("evalset: the server does not serve replays")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Filter is a saved ticket filter: the filter widget's fields, in the form
// the ticket list takes them (ticket.FilterQuery).
type Filter struct {
	Statuses    []string `json:"statuses,omitempty"`
	Priorities  []string `json:"priorities,omitempty"`
	Q           string   `json:"q,omitempty"`
	ClosedFrom  string   `json:"closed_from,omitempty"`
	ClosedTo    string   `json:"closed_to,omitempty"`
	CreatedFrom string   `json:"created_from,omitempty"`
	CreatedTo   string   `json:"created_to,omitempty"`
	UpdatedFrom string   `json:"updated_from,omitempty"`
	UpdatedTo   string   `json:"updated_to,omitempty"`
}

// listFilter is the ticket package's filter for f, which also validates it.
func (f *Filter) listFilter() (*ticket.ListFilter, error) {
	q := ticket.FilterQuery{
		Statuses: f.Statuses, Priorities: f.Priorities, Q: f.Q,
		ClosedFrom: f.ClosedFrom, ClosedTo: f.ClosedTo, CreatedFrom: f.CreatedFrom,
		CreatedTo: f.CreatedTo, UpdatedFrom: f.UpdatedFrom, UpdatedTo: f.UpdatedTo,
	}
	lf, err := q.Filter()
	if err != nil {
		return nil, invalid("%s", err.Error())
	}
	if err := lf.Validate(); err != nil {
		return nil, invalid("%s", err.Error())
	}
	return lf, nil
}

// Set is one eval set. Exactly one of Filter and TicketIDs is set.
type Set struct {
	ID          uuid.UUID
	Name        string
	Description string
	// Filter is re-resolved each time the set is used, so it follows the board.
	Filter *Filter
	// TicketIDs is a pinned list: later edits to those tickets do not change
	// which tickets the set holds.
	TicketIDs []int64
	CreatedBy *uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Definition is what a set holds, as a request gives it.
type Definition struct {
	Name        string
	Description string
	Filter      *Filter
	TicketIDs   []int64
}

// Batch is a replay batch run across a set.
type Batch struct {
	ID      uuid.UUID
	SetID   uuid.UUID
	BatchID uuid.UUID
	// VersionIDs are the variants replayed; a nil entry is the default variant.
	VersionIDs []*uuid.UUID
	Runs       int
	// TicketCount is how many tickets the batch added replays to.
	TicketCount int
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
}

// normalise checks a definition and returns it cleaned.
func (d *Definition) normalise() (*Definition, error) {
	name := strings.TrimSpace(d.Name)
	switch {
	case name == "":
		return nil, invalid("a name is required")
	case utf8.RuneCountInString(name) > MaxNameLen:
		return nil, invalid("a name is at most %d characters", MaxNameLen)
	case strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0:
		return nil, invalid("a name cannot hold control characters")
	case utf8.RuneCountInString(d.Description) > MaxDescriptionLen:
		return nil, invalid("a description is at most %d characters", MaxDescriptionLen)
	case (d.Filter == nil) == (d.TicketIDs == nil):
		return nil, invalid("give either a filter or a list of ticket ids, not both and not neither")
	}
	out := &Definition{Name: name, Description: d.Description, Filter: d.Filter}
	if d.Filter != nil {
		if _, err := d.Filter.listFilter(); err != nil {
			return nil, err
		}
	} else {
		ids, err := pinnedIDs(d.TicketIDs)
		if err != nil {
			return nil, err
		}
		out.TicketIDs = ids
	}
	return out, nil
}

// pinnedIDs drops duplicates, keeping order. An empty list is allowed: a set
// can be created before it has tickets, and running it says it is empty.
func pinnedIDs(in []int64) ([]int64, error) {
	if len(in) > MaxTickets {
		return nil, invalid("a set holds at most %d tickets", MaxTickets)
	}
	seen := map[int64]bool{}
	out := make([]int64, 0, len(in))
	for _, id := range in {
		if id < 1 {
			return nil, invalid("%d is not a ticket id", id)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}
