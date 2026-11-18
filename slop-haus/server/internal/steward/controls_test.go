package steward_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
)

type postedComment struct {
	steward uuid.UUID
	ticket  int64
	body    string
}

type commentLog struct {
	mu     sync.Mutex
	posted []postedComment
}

func (c *commentLog) CommentAsSteward(_ context.Context, id uuid.UUID, ticketID int64, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.posted = append(c.posted, postedComment{id, ticketID, body})
	return nil
}

func (c *commentLog) all() []postedComment {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]postedComment(nil), c.posted...)
}

type ownerRoles struct{ owners map[uuid.UUID]bool }

func (r ownerRoles) IsTenantOwner(_ context.Context, id, _ uuid.UUID) (bool, error) {
	return r.owners[id], nil
}

type reverter struct {
	mu    sync.Mutex
	asked []uuid.UUID
	err   error
}

func (r *reverter) RevertStewardApproval(_ context.Context, _ *authn.User, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, id)
	return r.err
}

type controlsRig struct {
	*applyRig
	comments *commentLog
	reverter *reverter
	controls *steward.Controls
	events   *audittest.Counter
	owner    *authn.User
	member   *authn.User
}

func newControlsRig(t *testing.T) *controlsRig {
	t.Helper()
	ar := newApplyRig(t)
	cr := &controlsRig{applyRig: ar, comments: &commentLog{}, reverter: &reverter{}}
	cr.owner = &authn.User{ID: dbtest.NewUser(t, ar.database), TenantID: ar.tenant, AuthTime: time.Now()}
	cr.member = &authn.User{ID: dbtest.NewUser(t, ar.database), TenantID: ar.tenant, AuthTime: time.Now()}
	recorder, counter := audittest.NewRecorder()
	cr.events = counter
	cr.controls = steward.NewControls(slog.NewTextHandler(io.Discard, nil), ar.store, ownerRoles{map[uuid.UUID]bool{cr.owner.ID: true}},
		cr.reverter, cr.comments, recorder, func() time.Time { return ar.now })
	ar.applier.WithComments(cr.comments, func(_ context.Context, kind steward.LinkKind, rec *steward.Record) string {
		return "https://board/" + string(kind) + "/" + rec.PendingActionID.String()
	})
	return cr
}

func TestAVetoDecisionGetsACommentWithItsReasonEvidenceAndVetoLink(t *testing.T) {
	cr := newControlsRig(t)

	_, a := cr.assessOpenTicket(t)

	posted := cr.comments.all()
	require.Len(t, posted, 1)
	assert.Equal(t, cr.console, posted[0].steward, "authored as the steward")
	assert.Equal(t, *cr.record(t, a).TicketID, posted[0].ticket)
	assert.Contains(t, posted[0].body, "[steward] ticket_ready: text is final")
	assert.Contains(t, posted[0].body, "- ticket_preparing (ok) status preparing")
	assert.Contains(t, posted[0].body, epoch.Add(10*time.Minute).UTC().Format(time.RFC3339), "the deadline")
	assert.Contains(t, posted[0].body, "Veto (pending): https://board/veto/"+a.ID.String())
	assert.NotContains(t, posted[0].body, "Undo")
}

func TestAnAppliedDecisionGetsACommentWithAnUndoLink(t *testing.T) {
	cr := newControlsRig(t)
	cr.settings.cfg.Tiers = steward.TierOverrides{"ticket_ready": steward.TierAuto}

	_, a := cr.assessOpenTicket(t)

	posted := cr.comments.all()
	require.Len(t, posted, 1, "one comment per action")
	assert.Contains(t, posted[0].body, "[steward] ticket_ready: text is final")
	assert.Contains(t, posted[0].body, "Undo (applied): https://board/undo/"+a.ID.String())
}

func TestADecisionAppliedAtItsDeadlineGetsAnUndoComment(t *testing.T) {
	cr := newControlsRig(t)
	_, a := cr.assessOpenTicket(t)

	cr.now = epoch.Add(10 * time.Minute)
	require.NoError(t, cr.applier.SweepOnce(cr.ctx))

	posted := cr.comments.all()
	require.Len(t, posted, 2)
	assert.Contains(t, posted[0].body, "Veto (pending)")
	assert.Contains(t, posted[1].body, "Undo (applied): https://board/undo/"+a.ID.String())
}

func TestVetoPreventsTheApplicationAndLeavesTheProposalToTheOwner(t *testing.T) {
	cr := newControlsRig(t)
	_, a := cr.assessOpenTicket(t)

	require.NoError(t, cr.controls.Veto(cr.ctx, cr.owner, a.ID))

	got := cr.record(t, a)
	require.NotNil(t, got.VetoedAt)
	require.NotNil(t, got.VetoedBy)
	assert.Equal(t, cr.owner.ID, *got.VetoedBy)
	assert.Equal(t, 1, cr.events.Count("steward_vetoed"))
	posted := cr.comments.all()
	assert.Contains(t, posted[len(posted)-1].body, "Vetoed by the owner")

	cr.now = epoch.Add(time.Hour)
	require.NoError(t, cr.applier.SweepOnce(cr.ctx))
	assert.Zero(t, cr.approver.calls(), "past the deadline, still not applied")
	now, err := cr.pending.Get(cr.ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StatePending, now.State)

	assert.ErrorIs(t, cr.controls.Veto(cr.ctx, cr.owner, a.ID), steward.ErrNotApplicable, "once")
}

func TestVetoIsCountedAsADisagreement(t *testing.T) {
	cr := newControlsRig(t)
	_, a := cr.assessOpenTicket(t)
	require.NoError(t, cr.controls.Veto(cr.ctx, cr.owner, a.ID))

	assert.Equal(t, agreement.Disagreed, agreement.ComparatorFor("ticket_ready")(cr.record(t, a)))
}

func TestVetoOfAnAppliedDecisionIsRefused(t *testing.T) {
	cr := newControlsRig(t)
	_, a := cr.assessOpenTicket(t)
	cr.now = epoch.Add(10 * time.Minute)
	require.NoError(t, cr.applier.SweepOnce(cr.ctx))

	assert.ErrorIs(t, cr.controls.Veto(cr.ctx, cr.owner, a.ID), steward.ErrNotApplicable)
	assert.Nil(t, cr.record(t, a).VetoedAt)
}

func TestUndoRevertsAndRecordsItAsADisagreement(t *testing.T) {
	cr := newControlsRig(t)
	_, a := cr.assessOpenTicket(t)
	cr.now = epoch.Add(10 * time.Minute)
	require.NoError(t, cr.applier.SweepOnce(cr.ctx))
	cr.now = epoch.Add(11 * time.Minute)

	require.NoError(t, cr.controls.Undo(cr.ctx, cr.owner, a.ID))

	assert.Equal(t, []uuid.UUID{a.ID}, cr.reverter.asked)
	got := cr.record(t, a)
	require.NotNil(t, got.UndoneAt)
	assert.Equal(t, cr.now, got.UndoneAt.UTC())
	assert.Equal(t, 1, cr.events.Count("steward_undone"))
	assert.Equal(t, agreement.Disagreed, agreement.ComparatorFor("ticket_ready")(got))
	assert.ErrorIs(t, cr.controls.Undo(cr.ctx, cr.owner, a.ID), steward.ErrNotApplicable, "once")
	assert.Len(t, cr.reverter.asked, 1)
}

func TestUndoThatCannotRevertRecordsNothing(t *testing.T) {
	cr := newControlsRig(t)
	_, a := cr.assessOpenTicket(t)
	cr.now = epoch.Add(10 * time.Minute)
	require.NoError(t, cr.applier.SweepOnce(cr.ctx))
	cr.reverter.err = steward.ErrNotRevertable

	assert.ErrorIs(t, cr.controls.Undo(cr.ctx, cr.owner, a.ID), steward.ErrNotRevertable)

	assert.Nil(t, cr.record(t, a).UndoneAt)
	assert.Zero(t, cr.events.Count("steward_undone"))
}

func TestUndoOfADecisionNotAppliedYetIsRefused(t *testing.T) {
	cr := newControlsRig(t)
	_, a := cr.assessOpenTicket(t)

	assert.ErrorIs(t, cr.controls.Undo(cr.ctx, cr.owner, a.ID), steward.ErrNotApplicable)
	assert.Empty(t, cr.reverter.asked)
}

func TestOnlyAnOwnerVetoesOrUndoes(t *testing.T) {
	cr := newControlsRig(t)
	_, pendingVeto := cr.assessOpenTicket(t)
	_, applied := cr.assessOpenTicket(t)
	cr.exec(t, `UPDATE steward_assessments SET applied_at = $2 WHERE pending_action_id = $1`, applied.ID, epoch)

	assert.ErrorIs(t, cr.controls.Veto(cr.ctx, cr.member, pendingVeto.ID), steward.ErrNotOwner)
	assert.ErrorIs(t, cr.controls.Undo(cr.ctx, cr.member, applied.ID), steward.ErrNotOwner)

	assert.Nil(t, cr.record(t, pendingVeto).VetoedAt)
	assert.Nil(t, cr.record(t, applied).UndoneAt)
	assert.Empty(t, cr.reverter.asked)
}

func TestVetoOfAProposalTheStewardNeverAssessedIsNotFound(t *testing.T) {
	cr := newControlsRig(t)
	other, _ := cr.proposal(t)

	assert.ErrorIs(t, cr.controls.Veto(cr.ctx, cr.owner, other.ID), steward.ErrNotFound)
}

func TestVetoOfADecisionThatIsNoLongerWaitingIsRefused(t *testing.T) {
	cases := map[string]string{
		"voided":        `UPDATE steward_assessments SET voided_at = now(), void_kind = 'checks' WHERE pending_action_id = $1`,
		"decided":       `UPDATE steward_assessments SET human_decision = 'rejected', decided_at = now() WHERE pending_action_id = $1`,
		"undone":        `UPDATE steward_assessments SET undone_at = now() WHERE pending_action_id = $1`,
		"no window yet": `UPDATE steward_assessments SET veto_deadline = NULL WHERE pending_action_id = $1`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			cr := newControlsRig(t)
			_, a := cr.assessOpenTicket(t)
			cr.exec(t, sql, a.ID)

			assert.ErrorIs(t, cr.controls.Veto(cr.ctx, cr.owner, a.ID), steward.ErrNotApplicable)
			assert.Nil(t, cr.record(t, a).VetoedAt)
		})
	}
}
