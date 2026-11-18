package pendingaction_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// fakeControls decides nothing: it answers what the test set and records the
// call. Who may veto, and what an undo reverts, is tested where it lives.
type fakeControls struct {
	err   error
	calls []string
	by    uuid.UUID
}

func (f *fakeControls) Veto(_ context.Context, owner *authn.User, id uuid.UUID) error {
	f.calls, f.by = append(f.calls, "veto "+id.String()), owner.ID
	return f.err
}

func (f *fakeControls) Undo(_ context.Context, owner *authn.User, id uuid.UUID) error {
	f.calls, f.by = append(f.calls, "undo "+id.String()), owner.ID
	return f.err
}

func TestStewardVetoAndUndoRoutesPassTheSignedInPersonAndMapRefusals(t *testing.T) {
	w := newWire(t)
	p := w.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	path := "/api/proposals/" + p.ID.String() + "/steward/"

	for _, action := range []string{"veto", "undo"} {
		rec := w.as(t, userToken, http.MethodPost, path+action, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, w.approver.ID, w.controls.by)
		assert.Equal(t, action+" "+p.ID.String(), w.controls.calls[len(w.controls.calls)-1])
	}

	for err, want := range map[error]int{
		pendingaction.ErrNotApprover:     http.StatusForbidden,
		pendingaction.ErrNotFound:        http.StatusNotFound,
		pendingaction.ErrNotPending:      http.StatusConflict,
		pendingaction.ErrNotRevertable:   http.StatusConflict,
		errors.New("the database broke"): http.StatusInternalServerError,
	} {
		w.controls.err = err
		for _, action := range []string{"veto", "undo"} {
			assert.Equal(t, want, w.as(t, userToken, http.MethodPost, path+action, "").Code, "%v on %s", err, action)
		}
	}
}
