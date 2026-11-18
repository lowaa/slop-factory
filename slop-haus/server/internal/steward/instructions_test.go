package steward_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

type pinned struct{ id *uuid.UUID }

func (p pinned) PinnedStewardInstructions(context.Context) (*uuid.UUID, error) { return p.id, nil }

func TestAssessmentRecordsTheStewardInstructionsVersionPinnedAtTheTime(t *testing.T) {
	ar := newApplyRig(t)
	version := uuid.New()
	ar.svc.WithInstructions(pinned{&version})

	rec, a := ar.assessOpenTicket(t)

	require.NotNil(t, rec.StewardInstructionsVersionID)
	assert.Equal(t, version, *rec.StewardInstructionsVersionID)
	stored := ar.record(t, a)
	require.NotNil(t, stored.StewardInstructionsVersionID, "it is stored, not only returned")
	assert.Equal(t, version, *stored.StewardInstructionsVersionID)
}

func TestAssessmentRecordsNoVersionWhenNoneIsPinned(t *testing.T) {
	ar := newApplyRig(t)
	ar.svc.WithInstructions(pinned{})

	rec, _ := ar.assessOpenTicket(t)

	assert.Nil(t, rec.StewardInstructionsVersionID)
}

type brokenPin struct{ err error }

func (b brokenPin) PinnedStewardInstructions(context.Context) (*uuid.UUID, error) { return nil, b.err }

func TestAssessmentFailsWhenThePinCannotBeRead(t *testing.T) {
	ar := newApplyRig(t)
	boom := errors.New("boom")
	ar.svc.WithInstructions(brokenPin{boom})
	a, _ := ar.proposal(t)
	ar.facts.revision = a.Basis.Revision

	_, err := ar.svc.Assess(ar.ctx, ar.console, &steward.Request{ProposalID: a.ID, Category: "open_ticket", Reason: "x"})

	assert.ErrorIs(t, err, boom)
	_, err = getByAction(ar.rig, a.ID)
	assert.ErrorIs(t, err, steward.ErrNotFound, "no assessment is kept")
}

var _ steward.Instructions = pinned{}

type fakeModels struct {
	run steward.RunConfig
	err error
}

func (f fakeModels) StewardRunConfig(context.Context, uuid.UUID) (steward.RunConfig, error) {
	return f.run, f.err
}

func TestAssessmentRecordsTheModelAndEffortTheStewardRanUnder(t *testing.T) {
	ar := newApplyRig(t)
	ar.svc.WithModels(fakeModels{run: steward.RunConfig{Model: "claude-opus-5-5", Effort: "high"}})

	rec, a := ar.assessOpenTicket(t)

	require.NotNil(t, rec.Model)
	assert.Equal(t, "claude-opus-5-5", *rec.Model)
	require.NotNil(t, rec.Effort)
	assert.Equal(t, "high", *rec.Effort)
	stored := ar.record(t, a)
	require.NotNil(t, stored.Model, "it is stored, not only returned")
	assert.Equal(t, "claude-opus-5-5", *stored.Model)
	assert.Equal(t, "high", *stored.Effort)
}

func TestAssessmentRecordsNoModelWhenTheConfigSetsNone(t *testing.T) {
	ar := newApplyRig(t)
	ar.svc.WithModels(fakeModels{})

	rec, _ := ar.assessOpenTicket(t)

	assert.Nil(t, rec.Model)
	assert.Nil(t, rec.Effort)
}

func TestAssessmentFailsWhenTheStewardModelCannotBeRead(t *testing.T) {
	ar := newApplyRig(t)
	boom := errors.New("boom")
	ar.svc.WithModels(fakeModels{err: boom})
	a, _ := ar.proposal(t)
	ar.facts.revision = a.Basis.Revision

	_, err := ar.svc.Assess(ar.ctx, ar.console, &steward.Request{ProposalID: a.ID, Category: "open_ticket", Reason: "x"})

	assert.ErrorIs(t, err, boom)
	_, err = getByAction(ar.rig, a.ID)
	assert.ErrorIs(t, err, steward.ErrNotFound, "no assessment is kept")
}
