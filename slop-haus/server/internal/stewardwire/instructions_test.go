package stewardwire_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/stewardwire"
)

type fakeInstructions struct {
	byKind map[fleetinstructions.Kind]*fleetinstructions.Version
}

func (f fakeInstructions) DefaultOf(_ context.Context, kind fleetinstructions.Kind) (*fleetinstructions.Version, error) {
	return f.byKind[kind], nil
}

// The assessment records the steward kind's pin, not the fleet one.
func TestPinnedInstructionsReadTheStewardKind(t *testing.T) {
	fleet := &fleetinstructions.Version{ID: uuid.New()}
	steward := &fleetinstructions.Version{ID: uuid.New()}
	p := stewardwire.NewPinnedInstructions(fakeInstructions{map[fleetinstructions.Kind]*fleetinstructions.Version{
		fleetinstructions.KindFleet: fleet, fleetinstructions.KindSteward: steward}})

	got, err := p.PinnedStewardInstructions(context.Background())
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, steward.ID, *got)

	none, err := stewardwire.NewPinnedInstructions(fakeInstructions{}).PinnedStewardInstructions(context.Background())
	require.NoError(t, err)
	assert.Nil(t, none)
}
