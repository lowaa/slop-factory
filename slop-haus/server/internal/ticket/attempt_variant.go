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

// ErrVariantLocked is a change of the variant of an attempt that has already
// been leased: after a handoff it shows as queued again, but a new variant
// would mix two configurations in one attempt.
var ErrVariantLocked = errors.New("ticket: the attempt has had a lease, so its variant can no longer change")

// ErrAttemptRequired is a change to one of a ticket's attempts, made to a
// ticket with several and none promoted, so the ticket does not say which.
var ErrAttemptRequired = errors.New("ticket: the ticket has several attempts and none is promoted: name the attempt")

// onlyAttempt is the ticket's sole attempt, for a ticket none of whose
// attempts is promoted; ErrAttemptRequired when it has several. Choosing
// among them is for the caller that can name one.
func onlyAttempt(ctx context.Context, q db.Querier, ticketID int64) (*AttemptRef, error) {
	rows, err := q.Query(ctx, `SELECT number FROM attempts WHERE ticket_id = $1 ORDER BY number LIMIT 2`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("read the attempts: %w", err)
	}
	numbers, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return nil, fmt.Errorf("read the attempts: %w", err)
	}
	switch len(numbers) {
	case 0:
		return nil, fmt.Errorf("ticket #%d: %w", ticketID, ErrNoPromotedAttempt)
	case 1:
		return &AttemptRef{TicketID: ticketID, Number: numbers[0]}, nil
	}
	return nil, fmt.Errorf("ticket #%d: %w", ticketID, ErrAttemptRequired)
}

// ErrInvalidVariant is a version the attempt cannot be routed to: an
// archived one. Archiving only hides a version from the list, so an attempt
// already routed to it keeps routing.
var ErrInvalidVariant = errors.New("ticket: that variant version cannot be chosen")

// ErrVariantNotFound is a version the tenant does not have.
var ErrVariantNotFound = errors.New("ticket: no such variant version")

// attemptEverLeased is true of an attempt aliased attempt that has had a
// lease. Released leases are pruned, so the lease alone would reopen the
// variant of an old handed-off attempt; the attempt's status history is not
// pruned, and records the move to in_progress that every claim makes.
func attemptEverLeased(attempt string) string {
	return `(EXISTS (SELECT 1 FROM leases el WHERE el.tenant_id = ` + attempt + `.tenant_id
		AND el.ticket_id = ` + attempt + `.ticket_id AND el.attempt_number = ` + attempt + `.number)
	OR EXISTS (SELECT 1 FROM attempt_status_changes es WHERE es.tenant_id = ` + attempt + `.tenant_id
		AND es.ticket_id = ` + attempt + `.ticket_id AND es.attempt_number = ` + attempt + `.number
		AND es.to_status = 'in_progress'))`
}

// AttemptVariantChange is what SetAttemptVariant replaced and set, for the
// audit record. A nil version is none.
type AttemptVariantChange struct {
	Attempt  AttemptRef
	Previous *variant.Version
	Now      *variant.Version
	// Changed is whether the attempt's variant is not what it was.
	Changed bool
}

// SetAttemptVariant routes the ticket's promoted attempt (else its only one)
// to a variant version, or clears it with nil, for any member. Only until the
// attempt's first lease (ErrVariantLocked). A ticket with several attempts
// and none promoted needs SetAttemptVariantAt (ErrAttemptRequired).
func (s *Service) SetAttemptVariant(ctx context.Context, userID uuid.UUID, ticketID int64, versionID *uuid.UUID) (*AttemptVariantChange, error) {
	return s.setAttemptVariant(ctx, userID, ticketID, nil, versionID)
}

// SetAttemptVariantAt is SetAttemptVariant for the named attempt of the
// ticket (ErrNoSuchAttempt when it has none).
func (s *Service) SetAttemptVariantAt(ctx context.Context, userID uuid.UUID, ticketID int64, attempt int, versionID *uuid.UUID) (*AttemptVariantChange, error) {
	return s.setAttemptVariant(ctx, userID, ticketID, &attempt, versionID)
}

func (s *Service) setAttemptVariant(ctx context.Context, userID uuid.UUID, ticketID int64, attempt *int, versionID *uuid.UUID) (*AttemptVariantChange, error) {
	change, err := s.store.SetAttemptVariant(ctx, ticketID, attempt, versionID)
	if err != nil {
		return nil, err
	}
	if s.events != nil && change.Changed {
		tenant, _ := db.TenantFrom(ctx)
		s.events.Record(ctx, audit.AttemptVariantChanged,
			"user_id", userID, "tenant", tenant, "ticket", ticketID, "attempt", change.Attempt.Number,
			"old", versionLabel(change.Previous), "new", versionLabel(change.Now))
	}
	return change, nil
}

func versionLabel(v *variant.Version) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%s v%d (%s)", v.Name, v.Version, v.ID)
}

// SetAttemptVariant holds the claim lock, as every claim does, so "no lease
// yet" cannot change between the check and the write: a claim either leased
// the attempt first (refused here) or sees the new variant when it matches.
// attempt names the attempt; nil is the promoted one, else the only one.
func (s *PostgresStore) SetAttemptVariant(ctx context.Context, ticketID int64, attempt *int, versionID *uuid.UUID) (*AttemptVariantChange, error) {
	var change *AttemptVariantChange
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if err := db.TakeAdvisoryLock(ctx, q, claimAdvisoryKey); err != nil {
			return err
		}
		// The ticket's row first, then the attempt's: the lock order AttemptRef names.
		if err := q.QueryRow(ctx, `SELECT id FROM tickets WHERE id = $1 FOR UPDATE`, ticketID).Scan(new(int64)); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
			}
			return fmt.Errorf("lock ticket: %w", err)
		}
		ref, err := variantAttempt(ctx, q, ticketID, attempt)
		if err != nil {
			return err
		}
		change = &AttemptVariantChange{Attempt: *ref}
		var current *uuid.UUID
		var leased bool
		if err := q.QueryRow(ctx, `
			SELECT a.variant_version_id, `+attemptEverLeased("a")+`
			  FROM attempts a WHERE a.ticket_id = $1 AND a.number = $2`,
			ticketID, ref.Number).Scan(&current, &leased); err != nil {
			return fmt.Errorf("read the attempt's variant: %w", err)
		}
		if current != nil {
			// Not AssignableVersion: an archived version it already names is
			// still its variant, for the audit record.
			if change.Previous, err = variantByID(ctx, q, *current); err != nil {
				return err
			}
		}
		if sameVariant(current, versionID) {
			change.Now = change.Previous
			return nil
		}
		if versionID != nil {
			if change.Now, err = assignableVersion(ctx, q, *versionID); err != nil {
				return err
			}
		}
		if leased {
			return ErrVariantLocked
		}
		change.Changed = true
		return writeAttempt(ctx, q, ref, &setList{clauses: []string{"variant_version_id = $3"}, args: []any{versionID}, first: 3})
	})
	if err != nil {
		return nil, err
	}
	return change, nil
}

func variantAttempt(ctx context.Context, q db.Querier, ticketID int64, attempt *int) (*AttemptRef, error) {
	if attempt != nil {
		var found int
		err := q.QueryRow(ctx, `SELECT number FROM attempts WHERE ticket_id = $1 AND number = $2`,
			ticketID, *attempt).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("#%d has no attempt %d: %w", ticketID, *attempt, ErrNoSuchAttempt)
		}
		if err != nil {
			return nil, fmt.Errorf("read the attempt: %w", err)
		}
		return &AttemptRef{TicketID: ticketID, Number: found}, nil
	}
	ref, err := promotedAttempt(ctx, q, ticketID)
	if errors.Is(err, ErrNoPromotedAttempt) {
		return onlyAttempt(ctx, q, ticketID)
	}
	return ref, err
}

func sameVariant(a, b *uuid.UUID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func variantByID(ctx context.Context, q db.Querier, id uuid.UUID) (*variant.Version, error) {
	var v variant.Version
	err := q.QueryRow(ctx, `SELECT id, name, version FROM variant_versions WHERE id = $1`, id).Scan(&v.ID, &v.Name, &v.Version)
	if err != nil {
		return nil, fmt.Errorf("read the attempt's variant version: %w", err)
	}
	return &v, nil
}
