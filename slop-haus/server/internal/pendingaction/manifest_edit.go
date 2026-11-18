package pendingaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ManifestPayloadVersion is the shape of ManifestEditPayload this code reads.
// A stored proposal of any other version is refused when approved, never
// guessed at: a change to the manifest's shape either converts old payloads
// explicitly or lets them fail.
const ManifestPayloadVersion = 1

// maxManifestBytes bounds what a proposal stores and the owner reads.
const maxManifestBytes = 256 << 10

// ManifestEditPayload is the whole manifest a proposal would put in place,
// and the revision it was made against.
type ManifestEditPayload struct {
	PayloadVersion int `json:"payload_version"`
	// BaseRevision is the manifest revision the proposer read (workspace_get).
	// It must be the current one when proposing and when approving.
	BaseRevision int64           `json:"base_revision"`
	Manifest     []manifest.Repo `json:"manifest"`
}

// ManifestEditor is the manifest as manifest_edit proposals read and change it.
type ManifestEditor interface {
	Get(ctx context.Context) (*manifest.Manifest, error)
	Revision(ctx context.Context) (int64, error)
	// LockRevision reads the revision and holds it for the transaction q is in.
	LockRevision(ctx context.Context, q db.Querier) (int64, error)
	// Apply saves repos as by's edit under the current rules, if the revision is still base.
	Apply(ctx context.Context, q db.Querier, by uuid.UUID, base int64, repos []manifest.Repo) (*manifest.Manifest, error)
}

// WithManifests lets manifest_edit proposals be made and approved. Without
// it they can be neither, so a Service built without it fails closed.
func WithManifests(editor ManifestEditor) Option {
	return func(s *Service) {
		s.manifests = editor
		WithTenantApplier(KindManifestEdit, &manifestApplier{editor: editor})(s)
	}
}

func normaliseManifestEdit(p Payload) (Payload, error) {
	if p.Body != "" || p.Status != "" || p.Decision != "" || p.Edit != nil {
		return Payload{}, invalid("a manifest edit takes only the manifest and its base revision")
	}
	m := p.ManifestEdit
	if m == nil {
		return Payload{}, invalid("a manifest edit needs a manifest")
	}
	// A proposer does not choose the version: the one it stores is this code's.
	if m.PayloadVersion != 0 && m.PayloadVersion != ManifestPayloadVersion {
		return Payload{}, invalid("payload_version %d is not supported", m.PayloadVersion)
	}
	if m.BaseRevision < 0 {
		return Payload{}, invalid("base_revision is a manifest revision, not %d", m.BaseRevision)
	}
	repos, err := manifest.Normalise(m.Manifest)
	if err != nil {
		return Payload{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if body, err := json.Marshal(repos); err != nil || len(body) > maxManifestBytes {
		return Payload{}, invalid("the manifest is over %d bytes", maxManifestBytes)
	}
	return Payload{ManifestEdit: &ManifestEditPayload{
		PayloadVersion: ManifestPayloadVersion, BaseRevision: m.BaseRevision, Manifest: repos,
	}}, nil
}

func manifestEditLine(a *Action) string {
	m := a.Payload.ManifestEdit
	if m == nil {
		return "Unknown action on the workspace."
	}
	return fmt.Sprintf("Replace the workspace manifest (revision %d when proposed) with the one below, as your own edit. "+
		"Its deploy-only rules loosen the merge gate and its shared paths loosen scope exclusivity for every agent.", m.BaseRevision)
}

// checkManifestBase refuses a proposal made against a manifest that has
// changed since the proposer read it.
func (s *Service) checkManifestBase(ctx context.Context, p Payload) error {
	if s.manifests == nil {
		return invalid("manifest edits are not available")
	}
	rev, err := s.manifests.Revision(ctx)
	if err != nil {
		return err
	}
	// Refused here, where the proposer sees it, not only at approval.
	if _, err := manifest.Normalise(p.ManifestEdit.Manifest); errors.Is(err, manifest.ErrInvalid) {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if base := p.ManifestEdit.BaseRevision; base != rev {
		return invalid("the manifest is at revision %d, not the %d this edit was made against: read it again and re-propose", rev, base)
	}
	return nil
}

// manifestApplier applies a manifest_edit proposal in the approval's
// transaction.
type manifestApplier struct{ editor ManifestEditor }

var _ TenantRechecker = (*manifestApplier)(nil)

// Recheck takes the manifest's lock and settles the proposal stale when the
// manifest has changed since. The lock is held to the end of the approval's
// transaction, so nothing saves between this and Apply.
func (m *manifestApplier) Recheck(ctx context.Context, tx ticket.TransitionTx, a *Action) (State, string, error) {
	if a.Payload.ManifestEdit == nil {
		return "", "", nil
	}
	rev, err := m.editor.LockRevision(ctx, tx.Querier())
	if err != nil {
		return "", "", err
	}
	if base := a.Payload.ManifestEdit.BaseRevision; base != rev {
		return StateStale, fmt.Sprintf("the manifest has changed since it was proposed: it was revision %d, it is %d", base, rev), nil
	}
	return "", "", nil
}

// Apply decodes the stored payload strictly -- unknown fields are refused --
// and runs today's manifest rules on it, so a proposal stored under older
// rules cannot put in place what the board would now refuse.
func (m *manifestApplier) Apply(ctx context.Context, tx ticket.TransitionTx, approver uuid.UUID, a *Action) error {
	q := tx.Querier()
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT payload FROM pending_actions WHERE id = $1`, a.ID).Scan(&raw); err != nil {
		return fmt.Errorf("read payload: %w", err)
	}
	var p Payload
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("the stored payload is not one this version reads: %w", err)
	}
	e := p.ManifestEdit
	if e == nil {
		return errors.New("the stored payload has no manifest")
	}
	if e.PayloadVersion != ManifestPayloadVersion {
		return fmt.Errorf("payload_version %d is not supported", e.PayloadVersion)
	}
	_, err := m.editor.Apply(ctx, q, approver, e.BaseRevision, e.Manifest)
	return err
}

// ManifestEditView is a manifest_edit proposal against the manifest as it is now.
type ManifestEditView struct {
	BaseRevision int64
	// Repos is the manifest the proposal would put in place.
	Repos []manifest.Repo
	// Changes is what that changes, against the manifest as it is now. Nil
	// once the proposal is decided: the manifest no longer says what it did.
	Changes []manifest.RepoChange
}

// manifestView compares a pending proposal to the live manifest, and says
// whether the manifest has moved on since it was made.
func (s *Service) manifestView(ctx context.Context, a *Action) (*ManifestEditView, string, error) {
	e := a.Payload.ManifestEdit
	if e == nil {
		return nil, "", nil
	}
	view := &ManifestEditView{BaseRevision: e.BaseRevision, Repos: e.Manifest}
	if a.State != StatePending || s.manifests == nil {
		return view, "", nil
	}
	live, err := s.manifests.Get(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("pending action: read manifest: %w", err)
	}
	var repos []manifest.Repo
	rev := int64(0)
	if live != nil {
		repos, rev = live.Repos, live.Revision
	} else if rev, err = s.manifests.Revision(ctx); err != nil {
		return nil, "", fmt.Errorf("pending action: read manifest: %w", err)
	}
	view.Changes = manifest.Diff(repos, e.Manifest)
	if rev != e.BaseRevision {
		return view, fmt.Sprintf("the manifest has changed since it was proposed: it was revision %d, it is %d", e.BaseRevision, rev), nil
	}
	return view, "", nil
}
