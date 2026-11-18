package landing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// PostgresHost is Host over the change store and the code host registry.
type PostgresHost struct {
	store change.Store
	hosts *codehost.Registry
}

func NewPostgresHost(store change.Store, hosts *codehost.Registry) *PostgresHost {
	return &PostgresHost{store: store, hosts: hosts}
}

func (h *PostgresHost) changes(rec *change.Record) (codehost.Changes, error) {
	c, ok := h.hosts.Changes(rec.Change.Host)
	if !ok {
		return nil, fmt.Errorf("%w: %s", change.ErrNoHost, rec.Change.Host)
	}
	return c, nil
}

// State reads whether the head is in the base on the host. After a merge with a
// merge commit the head is an ancestor of the base, so the branch has nothing
// the base lacks. A change merged on the host whose merge the store never
// recorded (the server died between the two) shows only there.
func (h *PostgresHost) State(ctx context.Context, tenant uuid.UUID, ticketID int64, attempt int) (*ChangeState, error) {
	rec, err := h.store.Current(ctx, ticketID, attempt)
	if err != nil {
		return nil, err
	}
	st := &ChangeState{URL: h.hosts.ChangeURL(rec.Change), Merged: rec.MergedAt != nil}
	changes, err := h.changes(rec)
	if err != nil {
		return nil, err
	}
	checks, err := changes.Checks(ctx, tenant, rec.Change)
	if err != nil {
		return nil, err
	}
	st.HeadSHA = checks.HeadSHA
	if st.Merged {
		return st, nil
	}
	cmp, err := changes.CompareBranches(ctx, tenant, rec.Change.InRepo(), rec.Base, rec.Head)
	switch {
	case errors.Is(err, codehost.ErrChangeNotFound):
		// A merged change's branch may be deleted by the host; the store has
		// no merge, so this is not proof either way.
		return st, nil
	case err != nil:
		return nil, err
	}
	st.Merged = cmp.Ahead == 0
	return st, nil
}

func (h *PostgresHost) UpdateBranch(ctx context.Context, tenant uuid.UUID, ticketID int64, attempt int, headSHA string) (*codehost.BranchUpdate, error) {
	rec, err := h.store.Current(ctx, ticketID, attempt)
	if err != nil {
		return nil, err
	}
	changes, err := h.changes(rec)
	if err != nil {
		return nil, err
	}
	return changes.UpdateBranch(ctx, tenant, &codehost.UpdateBranchRequest{Change: rec.Change, HeadSHA: headSHA})
}

func (h *PostgresHost) MarkMerged(ctx context.Context, ticketID int64, attempt int, by uuid.UUID) error {
	rec, err := h.store.Current(ctx, ticketID, attempt)
	if err != nil {
		return err
	}
	_, err = h.store.MarkMerged(ctx, rec.ID, by, "")
	return err
}

func (h *PostgresHost) record(ctx context.Context, row *LandingRow) (*change.Record, codehost.Changes, error) {
	rec, err := h.store.Current(ctx, row.TicketID, row.AttemptNumber)
	if err != nil {
		return nil, nil, err
	}
	changes, err := h.changes(rec)
	if err != nil {
		return nil, nil, err
	}
	return rec, changes, nil
}

// ChangedFiles reads the files the change brings to its base, for headSHA.
func (h *PostgresHost) ChangedFiles(ctx context.Context, row *LandingRow) ([]string, error) {
	rec, changes, err := h.record(ctx, row)
	if err != nil {
		return nil, err
	}
	diff, err := changes.Diff(ctx, tenantOf(ctx), rec.Change, row.HeadSHA)
	if err != nil {
		return nil, err
	}
	files := make([]string, len(diff.Files))
	for i := range diff.Files {
		files[i] = diff.Files[i].Path
	}
	return files, nil
}

func (h *PostgresHost) BaseTip(ctx context.Context, row *LandingRow) (string, error) {
	rec, changes, err := h.record(ctx, row)
	if err != nil {
		return "", err
	}
	head, err := changes.BranchHead(ctx, tenantOf(ctx), rec.Change.InRepo(), rec.Base)
	if err != nil {
		return "", err
	}
	return head.SHA, nil
}

func (h *PostgresHost) CreateBranch(ctx context.Context, row *LandingRow, branch, sha string) error {
	rec, changes, err := h.record(ctx, row)
	if err != nil {
		return err
	}
	return changes.CreateBranch(ctx, tenantOf(ctx), rec.Change.InRepo(), branch, sha)
}

func (h *PostgresHost) UpdateMember(ctx context.Context, row *LandingRow, headSHA string) (*codehost.BranchUpdate, error) {
	return h.UpdateBranch(ctx, tenantOf(ctx), row.TicketID, row.AttemptNumber, headSHA)
}

func (h *PostgresHost) MergeHead(ctx context.Context, row *LandingRow, branch, headSHA string) (*codehost.BranchMerge, error) {
	rec, changes, err := h.record(ctx, row)
	if err != nil {
		return nil, err
	}
	repo := rec.Change.InRepo()
	// The merge is of the branch, not of a commit: the head is read first so a
	// push since the row was queued is not tested as if it were the head that
	// was approved. A push between the two calls is the window the gate's tree
	// check after each member merges exists for.
	have, err := changes.BranchHead(ctx, tenantOf(ctx), repo, rec.Head)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(have.SHA, headSHA) {
		return nil, codehost.ErrHeadMoved
	}
	return changes.MergeBranch(ctx, tenantOf(ctx), repo, branch, rec.Head)
}

// BranchChecks reads the checks of the batch branch's head, in the repository
// of the row's change.
func (h *PostgresHost) BranchChecks(ctx context.Context, row *LandingRow, branch string) (*codehost.Checks, error) {
	rec, changes, err := h.record(ctx, row)
	if err != nil {
		return nil, err
	}
	return changes.BranchChecks(ctx, tenantOf(ctx), rec.Change.InRepo(), branch)
}

func (h *PostgresHost) OpenBatchChange(ctx context.Context, row *LandingRow, branch string) (codehost.ChangeRef, error) {
	rec, changes, err := h.record(ctx, row)
	if err != nil {
		return codehost.ChangeRef{}, err
	}
	return changes.OpenChange(ctx, tenantOf(ctx), &codehost.OpenRequest{
		Repo: rec.Change.InRepo(), Head: branch, Base: rec.Base, Draft: true,
		Title: "Merge queue " + branch,
		Body:  "The merge queue's batch branch, opened so that CI runs on it. The server closes it when the batch ends; do not merge it.",
	})
}

func (h *PostgresHost) CloseBatchChange(ctx context.Context, row *LandingRow, ref codehost.ChangeRef) error {
	_, changes, err := h.record(ctx, row)
	if err != nil {
		return err
	}
	return changes.CloseChange(ctx, tenantOf(ctx), ref)
}

func (h *PostgresHost) DeleteBranch(ctx context.Context, row *LandingRow, branch string) error {
	rec, changes, err := h.record(ctx, row)
	if err != nil {
		return err
	}
	return changes.DeleteBranch(ctx, tenantOf(ctx), rec.Change.InRepo(), branch)
}

func tenantOf(ctx context.Context) uuid.UUID {
	id, _ := db.TenantFrom(ctx)
	return id
}

var (
	_ BatchHost   = (*PostgresHost)(nil)
	_ BatchChecks = (*PostgresHost)(nil)
)
