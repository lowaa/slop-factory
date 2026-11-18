package githubhook_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
)

func TestLatestAppDelivery_IsTheTenantsNewestAndNeverAnotherTenants(t *testing.T) {
	f := newFixture(t)
	other := f.newTenant()
	store := githubhook.NewPostgresStore(f.database)
	begin := func(tn *tenantFixture, d *githubhook.Delivery) {
		begun, err := store.BeginDelivery(tn.ctx, d)
		require.NoError(t, err)
		require.True(t, begun)
	}
	begin(f.a, &githubhook.Delivery{ID: "ping-1", InstallationID: 7, Event: "ping"})
	begin(f.a, &githubhook.Delivery{ID: "review-1", InstallationID: 7, Event: "pull_request_review", Action: "submitted"})
	begin(other, &githubhook.Delivery{ID: "other-1", InstallationID: 7, Event: "pull_request", Action: "closed"})

	newest, err := store.LatestAppDelivery(f.a.ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, newest)
	assert.Equal(t, "review-1", newest.ID, "another tenant's delivery is not this tenant's")

	review, err := store.LatestAppDelivery(f.a.ctx, []string{"pull_request", "pull_request_review"})
	require.NoError(t, err)
	require.NotNil(t, review)
	assert.Equal(t, "pull_request_review", review.Event)

	none, err := store.LatestAppDelivery(f.a.ctx, []string{"pull_request"})
	require.NoError(t, err)
	assert.Nil(t, none, "the other tenant's pull_request does not count")

	empty := f.newTenant()
	got, err := store.LatestAppDelivery(empty.ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}
