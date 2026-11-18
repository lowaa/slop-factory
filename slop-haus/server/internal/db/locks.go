package db

import (
	"context"
	"fmt"
)

// Postgres advisory lock keys.
//
// They live here, in one block, because the advisory key space is
// *database-wide*: two packages each minting their own constant is exactly
// how two unrelated code paths end up silently serialized against each
// other, or -- worse -- how a key is duplicated and a lock that looks
// exclusive is not. A per-package constant hides that from the one place
// it could be noticed.
const (
	// ClaimLockKey serializes every claim, and anything that must not
	// interleave with one.
	//
	// It is one constant per tenant (see TakeAdvisoryLock), never derived
	// from a ticket or agent id: a per-ticket key looks like a finer-grained optimisation and
	// would silently destroy the property the lock exists for, since two
	// agents claiming different tickets would take different keys, run
	// concurrently, and each evaluate scope conflicts against a queue the
	// other is mutating.
	//
	// Held by the claim path (internal/ticket) and by settings writes
	// (internal/settings), which is not two unrelated uses of one key: the
	// fleet's processing switch is read inside the claim transaction, so a
	// write to it that did not take this lock could commit between that
	// read and the grant -- handing out work after the fleet was paused.
	ClaimLockKey int64 = 0x5109_4841_5553_0001

	// LLMUsageLockKey serializes the usage reports for one transcript
	// (see TakeSubjectAdvisoryLock, with the agent and transcript as the
	// subject). Reports are full totals, and each must read what the one
	// before it stored: two that both read the same previous totals would
	// both count the same tokens.
	LLMUsageLockKey int64 = 0x5109_4841_5553_0002

	// AttemptVerdictLockKey serializes the verdicts set on one attempt (see
	// TakeSubjectAdvisoryLock, with the ticket and attempt as the subject).
	// Each reads the verdict it replaces before appending its own, so two
	// that interleaved would both name the same one as replaced.
	AttemptVerdictLockKey int64 = 0x5109_4841_5553_0003
)

// TakeAdvisoryLock takes a transaction-scoped advisory lock for the
// context's tenant, blocking until it is available and releasing it when
// the transaction ends.
//
// Scoped to the tenant: the claim lock exists so two of one fleet's agents
// never evaluate the queue at once, and another tenant's fleet has its own
// queue. Keyed on a hash of key and tenant together -- one 64-bit key,
// rather than the two-int form, which would truncate key.
//
// Transaction-scoped (pg_advisory_xact_lock) rather than session-scoped:
// a session lock outlives a failed transaction and has to be unlocked by
// hand, so any path that returns early leaks it for the life of the
// pooled connection.
func TakeAdvisoryLock(ctx context.Context, q Querier, key int64) error {
	tenantID, err := TenantFrom(ctx)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, $2))`, tenantID.String(), key); err != nil {
		return fmt.Errorf("db: take advisory lock %#x: %w", key, err)
	}
	return nil
}

// TakeSubjectAdvisoryLock is TakeAdvisoryLock for one subject within the
// tenant -- one transcript, say -- rather than the whole tenant, so
// unrelated work under the same key does not wait on each other.
func TakeSubjectAdvisoryLock(ctx context.Context, q Querier, key int64, subject string) error {
	tenantID, err := TenantFrom(ctx)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1 || '/' || $2, $3))`, tenantID.String(), subject, key); err != nil {
		return fmt.Errorf("db: take advisory lock %#x: %w", key, err)
	}
	return nil
}
