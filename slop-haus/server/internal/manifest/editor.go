package manifest

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Editor is the manifest as a proposal's approval changes it: inside the
// approver's transaction, against the revision the proposal was made on.
// Who may approve is the approval's rule, not the editor's.
type Editor struct{ store Store }

func NewEditor(store Store) *Editor { return &Editor{store: store} }

// Get is the tenant's manifest, nil while it has declared none.
func (e *Editor) Get(ctx context.Context) (*Manifest, error) { return e.store.Get(ctx) }

// Revision is the tenant's current revision.
func (e *Editor) Revision(ctx context.Context) (int64, error) { return e.store.Revision(ctx) }

// LockRevision is the revision, held until q's transaction ends.
func (e *Editor) LockRevision(ctx context.Context, q db.Querier) (int64, error) {
	return e.store.LockRevision(ctx, q)
}

// Apply saves repos as the manifest, as by's edit, under today's rules and
// only if the revision is still base.
func (e *Editor) Apply(ctx context.Context, q db.Querier, by uuid.UUID, base int64, repos []Repo) (*Manifest, error) {
	clean, err := Normalise(repos)
	if err != nil {
		return nil, err
	}
	return e.store.PutAt(ctx, q, clean, by, base)
}
