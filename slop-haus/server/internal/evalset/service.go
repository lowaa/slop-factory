package evalset

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Tickets is the ticket package as the eval sets use it: listing by a filter,
// and the replay batch, whose rules (cap, confirmation, per-batch maximum, a
// ticket's own refusals) stay the ticket package's.
type Tickets interface {
	List(ctx context.Context, filter *ticket.ListFilter) ([]ticket.Ticket, error)
	AddReplayBatch(ctx context.Context, userID uuid.UUID, req *ticket.ReplayBatchRequest) (*ticket.ReplayBatchResult, error)
}

// Service is the rules: only people use eval sets, and running one is a
// replay batch of the ticket service's.
type Service struct {
	store   Store
	tickets Tickets
	replays bool
	logger  *slog.Logger
}

// NewService serves eval sets. replays says whether the server serves
// replays; running a set is refused when it does not.
func NewService(store Store, tickets Tickets, replays bool, logHandler slog.Handler) *Service {
	return &Service{store: store, tickets: tickets, replays: replays, logger: slog.New(logHandler)}
}

func (s *Service) List(ctx context.Context, who *authn.User) ([]Set, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	return s.store.List(ctx)
}

func (s *Service) Get(ctx context.Context, who *authn.User, id uuid.UUID) (*Set, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	return s.store.Get(ctx, id)
}

func (s *Service) Create(ctx context.Context, who *authn.User, def *Definition) (*Set, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	def, err := def.normalise()
	if err != nil {
		return nil, err
	}
	return s.store.Create(ctx, def, who.ID)
}

// Replace overwrites a set's name, description and what it holds.
func (s *Service) Replace(ctx context.Context, who *authn.User, id uuid.UUID, def *Definition) (*Set, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	def, err := def.normalise()
	if err != nil {
		return nil, err
	}
	return s.store.Replace(ctx, id, def, nil)
}

func (s *Service) Delete(ctx context.Context, who *authn.User, id uuid.UUID) error {
	if err := authn.RequirePerson(who); err != nil {
		return err
	}
	return s.store.Delete(ctx, id)
}

// PinMatches turns a filter set into a pinned list of the tickets matching
// the filter now. A set that is already pinned is returned as it is.
func (s *Service) PinMatches(ctx context.Context, who *authn.User, id uuid.UUID) (*Set, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	set, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if set.Filter == nil {
		return set, nil
	}
	ids, err := s.resolve(ctx, set)
	if err != nil {
		return nil, err
	}
	return s.store.Replace(ctx, id, &Definition{Name: set.Name, Description: set.Description, TicketIDs: ids}, &set.UpdatedAt)
}

// Resolve is the ids of the tickets the set holds now: its pinned list, or
// what its filter matches.
func (s *Service) Resolve(ctx context.Context, who *authn.User, id uuid.UUID) ([]int64, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	set, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.resolve(ctx, set)
}

func (s *Service) resolve(ctx context.Context, set *Set) ([]int64, error) {
	if set.Filter == nil {
		return set.TicketIDs, nil
	}
	filter, err := set.Filter.listFilter()
	if err != nil {
		return nil, err
	}
	// One more than the most, to tell a set that fits from one that is cut off.
	filter.Limit, filter.Summary = MaxTickets+1, true
	tickets, err := s.tickets.List(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("eval sets: resolve filter: %w", err)
	}
	if len(tickets) > MaxTickets {
		return nil, invalid("the filter matches more than %d tickets; narrow it", MaxTickets)
	}
	ids := make([]int64, len(tickets))
	for i := range tickets {
		ids[i] = tickets[i].ID
	}
	return ids, nil
}

// RunRequest asks for Runs replays of every ticket in the set under every
// variant, as ticket.ReplayBatchRequest does.
type RunRequest struct {
	// VersionIDs are the variant versions; a nil entry is the default variant.
	VersionIDs []*uuid.UUID
	Runs       int
	// ConfirmAttempts must equal the set's tickets x variants x runs.
	ConfirmAttempts int
}

// RunResult is the batch the run created. Tickets the replay refused are
// listed with the reason; the rest were added.
type RunResult struct {
	Batch   *ticket.ReplayBatchResult
	History *Batch
}

// Run replays the set's tickets in one batch through the ticket service, so
// its cap, confirmation and per-batch maximum apply unchanged. A ticket it
// cannot replay is in the result's Refused with its reason. A batch that
// added anything is written to the set's history.
func (s *Service) Run(ctx context.Context, who *authn.User, id uuid.UUID, req *RunRequest) (*RunResult, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	if !s.replays {
		return nil, ErrReplaysDisabled
	}
	set, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	ids, err := s.resolve(ctx, set)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrEmpty
	}
	// The history says what the ticket service ran, which drops repeats.
	versions := uniqueVersions(req.VersionIDs)
	batch, err := s.tickets.AddReplayBatch(ctx, who.ID, &ticket.ReplayBatchRequest{
		TicketIDs: ids, VersionIDs: versions, Runs: req.Runs, ConfirmAttempts: req.ConfirmAttempts,
	})
	if err != nil {
		return nil, err
	}
	res := &RunResult{Batch: batch}
	if len(batch.Added) == 0 {
		return res, nil
	}
	// The replays are committed whether or not the caller is still there, so
	// the record of them is written regardless.
	res.History, err = s.store.RecordBatch(context.WithoutCancel(ctx), &Batch{
		SetID: id, BatchID: batch.BatchID, VersionIDs: versions, Runs: req.Runs,
		TicketCount: len(batch.Added), CreatedBy: &who.ID,
	})
	if err != nil {
		// The replays are already added and cannot be taken back; the batch
		// id is logged so its history row can be written by hand.
		s.logger.ErrorContext(ctx, "record an eval set's batch", "eval_set", id, "batch_id", batch.BatchID, "err", err)
		return nil, fmt.Errorf("the replays were added (batch %s) but could not be recorded in the set's history: %w", batch.BatchID, err)
	}
	return res, nil
}

// History is the batches run across the set, newest first.
func (s *Service) History(ctx context.Context, who *authn.User, id uuid.UUID) ([]Batch, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	return s.store.Batches(ctx, id)
}

func uniqueVersions(in []*uuid.UUID) []*uuid.UUID {
	seen := map[uuid.UUID]bool{}
	hasDefault := false
	var out []*uuid.UUID
	for _, v := range in {
		switch {
		case v == nil && hasDefault, v != nil && seen[*v]:
		case v == nil:
			hasDefault = true
			out = append(out, v)
		default:
			seen[*v] = true
			out = append(out, v)
		}
	}
	return out
}
