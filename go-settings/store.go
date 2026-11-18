package settings

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Stored is one row of the log. Rows are kept deliberately, as the audit trail
// of every change; reads stay cheap through the table's index, so the tables
// are not pruned.
type Stored struct {
	Name      string
	Value     Value
	CreatedAt time.Time
}

// Change is one row of a setting's history.
type Change struct {
	ID        int64
	Value     Value
	CreatedAt time.Time
	// EarlierMembership marks a row written before the key's current
	// membership began: history only, never the current value. Always false
	// for a scope without a membership rule.
	EarlierMembership bool
	// ChangedBy and ChangedByDeleted are set only for a scope that records who
	// made a change. ChangedBy is nil when no person is on record: the account
	// is gone (ChangedByDeleted), or the row predates history being kept.
	ChangedBy        *uuid.UUID
	ChangedByDeleted bool
}

// Store is an append-only log of settings for one scope, keyed by the owner
// the scope names (a user, or a tenant). The caller has already checked name
// and value against its Registry.
type Store interface {
	// Append writes a new row and returns it.
	Append(ctx context.Context, key uuid.UUID, name string, v Value) (*Stored, error)
	// Latest returns the newest row of each of the given names; a name with
	// no row is absent.
	Latest(ctx context.Context, key uuid.UUID, names []string) ([]Stored, error)
	// History returns the key's rows for a name, newest first, those with an
	// id below before (0 for the newest), at most limit.
	History(ctx context.Context, key uuid.UUID, name string, before int64, limit int) ([]Change, error)
}

// ActorStore is a Store that can record who made a change, for a scope whose
// rows carry that.
type ActorStore interface {
	Store
	AppendBy(ctx context.Context, key uuid.UUID, name string, v Value, by uuid.UUID) (*Stored, error)
}
