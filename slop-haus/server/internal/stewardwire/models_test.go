package stewardwire_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/stewardwire"
)

// tenantConfig resolves with the real EffectiveFor, so the steward gets the same layering a worker does.
type tenantConfig struct{ t *harnessconfig.Tenant }

func (c tenantConfig) Effective(_ context.Context, id uuid.UUID) (harnessconfig.Effective, error) {
	return c.t.EffectiveFor(id), nil
}

func claude(model, effort string) harnessconfig.Values {
	return harnessconfig.Values{"claude": {"model": model, "effort": effort}}
}

func TestStewardModelFollowsTenantDefaultsUntilItHasItsOwn(t *testing.T) {
	steward := uuid.New()
	tenant := &harnessconfig.Tenant{
		Defaults:  &harnessconfig.Scope{Values: claude("claude-sonnet-5-5", "medium"), Revision: 1},
		Overrides: map[uuid.UUID]*harnessconfig.Scope{},
	}
	models := stewardwire.NewConfigModels(tenantConfig{tenant})

	got, err := models.StewardRunConfig(context.Background(), steward)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5-5", got.Model)
	assert.Equal(t, "medium", got.Effort)

	// Its own setting wins; a setting it leaves out still takes the default.
	tenant.Overrides[steward] = &harnessconfig.Scope{AgentID: &steward, Values: harnessconfig.Values{"claude": {"model": "claude-opus-5-5"}}, Revision: 2}
	got, err = models.StewardRunConfig(context.Background(), steward)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5-5", got.Model)
	assert.Equal(t, "medium", got.Effort)
}

func TestStewardModelIsEmptyWhenNothingIsConfigured(t *testing.T) {
	got, err := stewardwire.NewConfigModels(tenantConfig{&harnessconfig.Tenant{}}).StewardRunConfig(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Empty(t, got.Model)
	assert.Empty(t, got.Effort)
}
