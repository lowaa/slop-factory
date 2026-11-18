package main

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/assist"
)

type fixedRoles struct{ owner bool }

func (f fixedRoles) IsTenantOwner(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return f.owner, nil
}

// Only an owner reaches assist: the nil service would panic if it were asked.
func TestDraftApplier_RefusesAMemberBeforeAskingAssist(t *testing.T) {
	t.Parallel()
	a := &draftApplier{roles: fixedRoles{owner: false}}
	err := a.ApplyDraft(context.Background(), &authn.User{ID: uuid.New(), TenantID: uuid.New()}, uuid.New())
	require.ErrorIs(t, err, pendingaction.ErrNotApprover)
}

func TestMapAssistErr_NamesEachOutcomeInTheApprovalsTerms(t *testing.T) {
	t.Parallel()
	for in, want := range map[error]error{
		assist.ErrNotFound:         pendingaction.ErrNotFound,
		assist.ErrNotApplicable:    pendingaction.ErrInvalid,
		assist.ErrAlreadyApplied:   pendingaction.ErrNotPending,
		steward.ErrNotPending:      pendingaction.ErrNotPending,
		assist.ErrStale:            pendingaction.ErrRefused,
		assist.ErrNotAssist:        pendingaction.ErrRefused,
		steward.ErrApprovalRefused: pendingaction.ErrRefused,
	} {
		assert.ErrorIs(t, mapAssistErr(in), want, "%v", in)
		assert.ErrorIs(t, mapAssistErr(in), in)
	}
	other := errors.New("the database broke")
	assert.Equal(t, other, mapAssistErr(other))
	assert.NoError(t, mapAssistErr(nil))
}
