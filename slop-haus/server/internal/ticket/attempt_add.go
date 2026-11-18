package ticket

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/variant"
)

// MaxLiveAttempts is how many attempts of one ticket may be live at once.
const MaxLiveAttempts = 5

// ErrAttemptCap is an add to a ticket that already has MaxLiveAttempts live
// attempts.
var ErrAttemptCap = fmt.Errorf("ticket: the ticket already has %d live attempts", MaxLiveAttempts)

// ErrNoAttemptsOnStatus is an ordinary add to a ticket that is done, or any
// add to one that is cancelled. A done ticket takes only replays.
var ErrNoAttemptsOnStatus = errors.New("ticket: a done or cancelled ticket takes no new attempt")

// ErrReplayNotDone is a replay of a ticket that is neither done nor parked: a
// live ticket is worked by ordinary attempts.
var ErrReplayNotDone = errors.New("ticket: only a done or parked ticket can be replayed")

// addBarrier is a test-only seam, like claimBarrier. Production leaves it
// nil. It is called inside AddAttempt's transaction once the ticket's number
// is read and before it is used, so a test can hold one add there and show
// another cannot take the same number.
var addBarrier func()

// AttemptAdded is what adding an attempt did, for the audit record.
type AttemptAdded struct {
	Number  int
	Shadow  bool
	Replay  bool
	Variant *variant.Version
	// DemotedAttempt is the number of the promoted attempt that lost its
	// promotion because it had not started, or 0.
	DemotedAttempt int
}

// AddAttempt adds an attempt to the ticket, for any member, naming a variant
// version for it if versionID is set.
func (s *Service) AddAttempt(ctx context.Context, userID uuid.UUID, ticketID int64, versionID *uuid.UUID) (*AttemptAdded, error) {
	added, err := s.AddAttempts(ctx, userID, ticketID, []*uuid.UUID{versionID})
	if err != nil {
		return nil, err
	}
	return added[0], nil
}

// AddReplays adds one replay per entry of versionIDs to a done ticket, all or
// none. A replay is always a shadow that starts at the ticket's original base
// commit and reads the ticket as its snapshot froze it; it is never promoted,
// holds no scope and leaves the ticket done.
func (s *Service) AddReplays(ctx context.Context, userID uuid.UUID, ticketID int64, versionIDs []*uuid.UUID) ([]*AttemptAdded, error) {
	return s.addAttempts(ctx, userID, ticketID, versionIDs, true)
}

// AddAttempts adds one attempt per entry of versionIDs (nil for none), all or
// none: the Add-attempt form submits several rows at once. At most
// MaxLiveAttempts rows.
func (s *Service) AddAttempts(ctx context.Context, userID uuid.UUID, ticketID int64, versionIDs []*uuid.UUID) ([]*AttemptAdded, error) {
	return s.addAttempts(ctx, userID, ticketID, versionIDs, false)
}

func (s *Service) addAttempts(ctx context.Context, userID uuid.UUID, ticketID int64, versionIDs []*uuid.UUID, replay bool) ([]*AttemptAdded, error) {
	return s.addAttemptsInBatch(ctx, userID, ticketID, versionIDs, replay, nil)
}

// addAttemptsInBatch is addAttempts with the id of the replay batch the rows
// belong to, or nil for none.
func (s *Service) addAttemptsInBatch(ctx context.Context, userID uuid.UUID, ticketID int64, versionIDs []*uuid.UUID, replay bool, batchID *uuid.UUID) ([]*AttemptAdded, error) {
	switch {
	case len(versionIDs) == 0:
		return nil, ErrEmptyBatch
	case len(versionIDs) > MaxLiveAttempts:
		return nil, fmt.Errorf("%w: %d attempts, at most %d", ErrBatchTooLarge, len(versionIDs), MaxLiveAttempts)
	}
	added, err := s.store.AddAttempts(WithUser(ctx, userID), ticketID, versionIDs, replay, batchID)
	if err != nil {
		return nil, err
	}
	// After the commit, so a refused batch leaves no record of attempts that
	// do not exist.
	if s.events != nil {
		tenant, _ := db.TenantFrom(ctx)
		for _, a := range added {
			s.events.Record(ctx, audit.AttemptAdded,
				"user_id", userID, "tenant", tenant, "ticket", ticketID, "attempt", a.Number,
				"shadow", a.Shadow, "replay", a.Replay, "batch_id", batchID, "variant", versionLabel(a.Variant), "demoted_attempt", a.DemotedAttempt)
		}
	}
	return added, nil
}

// attemptStart is what the add decides on, per attempt.
type attemptStart struct {
	number   int
	status   string
	promoted bool
	started  bool
	replay   bool
}

// AddAttempts holds the claim lock, as every claim does, then the ticket's
// row, then its attempts': whether the promoted attempt has started cannot
// change between the check and the write, so a claim either started it first
// (the new attempts are shadows) or finds it demoted and the new ones queued.
// Every row is checked before any is written, and they share one transaction,
// so a refused batch adds nothing. A promoted attempt that has not started
// loses its promotion once for the whole batch.
func (s *PostgresStore) AddAttempts(ctx context.Context, ticketID int64, versionIDs []*uuid.UUID, replay bool, batchID *uuid.UUID) ([]*AttemptAdded, error) {
	var added []*AttemptAdded
	err := s.db.InTx(ctx, func(q db.Querier) error {
		added = nil
		if err := db.TakeAdvisoryLock(ctx, q, claimAdvisoryKey); err != nil {
			return err
		}
		var status Status
		var number int
		var baseCommit *string
		err := q.QueryRow(ctx, `SELECT status, next_attempt_number, base_commit FROM tickets WHERE id = $1 FOR UPDATE`,
			ticketID).Scan(&status, &number, &baseCommit)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
		}
		if err != nil {
			return fmt.Errorf("lock ticket: %w", err)
		}
		switch {
		case replay && !replayableStatus(status):
			return fmt.Errorf("ticket #%d is %s: %w", ticketID, status, ErrReplayNotDone)
		case replay && baseCommit == nil:
			return fmt.Errorf("ticket #%d: %w", ticketID, ErrNoBase)
		case !replay && (status == StatusDone || status == StatusCancelled):
			return fmt.Errorf("ticket #%d is %s: %w", ticketID, status, ErrNoAttemptsOnStatus)
		}
		if replay {
			// The frozen view a replay reads; without one the claim could
			// show it nothing.
			var snapshotted bool
			if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ticket_snapshots WHERE ticket_id = $1)`, ticketID).Scan(&snapshotted); err != nil {
				return fmt.Errorf("look for the ticket's snapshot: %w", err)
			}
			if !snapshotted {
				return fmt.Errorf("ticket #%d: %w", ticketID, ErrNoSnapshot)
			}
		}
		if addBarrier != nil {
			addBarrier()
		}
		for i, versionID := range versionIDs {
			a := &AttemptAdded{Number: number + i}
			if versionID != nil {
				if a.Variant, err = assignableVersion(ctx, q, *versionID); err != nil {
					return err
				}
			}
			added = append(added, a)
		}
		attempts, err := lockAttempts(ctx, q, ticketID)
		if err != nil {
			return err
		}
		live := 0
		var promoted *attemptStart
		for i := range attempts {
			switch attempts[i].status {
			case "queued", "in_progress", "in_merge_queue", "discussion":
				live++
			}
			if attempts[i].promoted {
				promoted = &attempts[i]
			}
		}
		if live+len(added) > MaxLiveAttempts {
			return fmt.Errorf("ticket #%d has %d live attempts and %d more were asked for: %w",
				ticketID, live, len(added), ErrAttemptCap)
		}

		if err := userMover(userFrom(ctx), "attempt added").set(ctx, q); err != nil {
			return err
		}
		shadow := replay
		switch {
		case replay:
			// Never touches the promoted attempt: the ticket stays as it is.
		case promoted != nil && promoted.started:
			if baseCommit == nil {
				return fmt.Errorf("ticket #%d: %w", ticketID, ErrNoBase)
			}
			shadow = true
		case promoted != nil:
			if err := writeAttempt(ctx, q, &AttemptRef{TicketID: ticketID, Number: promoted.number}, &setList{
				clauses: []string{"promoted_at = NULL", "promoted_by_user_id = NULL", "promoted_by_user_id_deleted = false", "updated_at = now()"}, first: 3,
			}); err != nil {
				return err
			}
			added[0].DemotedAttempt = promoted.number
		}
		for _, a := range added {
			a.Shadow = shadow
			a.Replay = replay
			var variantID *uuid.UUID
			if a.Variant != nil {
				variantID = &a.Variant.ID
			}
			if _, err := q.Exec(ctx, `
				INSERT INTO attempts (ticket_id, number, status, shadow, replay, variant_version_id, replay_batch_id)
				VALUES ($1, $2, 'queued', $3, $4, $5, $6)`, ticketID, a.Number, a.Shadow, a.Replay, variantID, batchID); err != nil {
				return fmt.Errorf("insert attempt: %w", err)
			}
		}
		if _, err := q.Exec(ctx, `UPDATE tickets SET next_attempt_number = next_attempt_number + $2 WHERE id = $1`,
			ticketID, len(added)); err != nil {
			return fmt.Errorf("take the attempt numbers: %w", err)
		}
		if replay {
			// A replay never counts toward the ticket's status.
			return nil
		}
		// A person's preparing and parked are not the attempts' to move.
		_, err = deriveStatus(ctx, q, "t.id = $1 AND "+inQueueLifecycle, ticketID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return added, nil
}

// lockAttempts reads the ticket's attempts in number order, taking their
// rows after the ticket's (the lock order AttemptRef names).
func lockAttempts(ctx context.Context, q db.Querier, ticketID int64) ([]attemptStart, error) {
	rows, err := q.Query(ctx, `
		SELECT number, status, promoted_at IS NOT NULL, started_revision IS NOT NULL, replay
		  FROM attempts WHERE ticket_id = $1 ORDER BY number FOR UPDATE`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("lock the attempts: %w", err)
	}
	attempts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (attemptStart, error) {
		var a attemptStart
		err := row.Scan(&a.number, &a.status, &a.promoted, &a.started, &a.replay)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the attempts: %w", err)
	}
	return attempts, nil
}

// assignableVersion is variant.AssignableVersion with this package's errors.
func assignableVersion(ctx context.Context, q db.Querier, id uuid.UUID) (*variant.Version, error) {
	v, err := variant.AssignableVersion(ctx, q, id)
	switch {
	case errors.Is(err, variant.ErrNotFound):
		return nil, ErrVariantNotFound
	case errors.Is(err, variant.ErrInvalid):
		return nil, fmt.Errorf("%w: %s", ErrInvalidVariant, err.Error())
	}
	return v, err
}
