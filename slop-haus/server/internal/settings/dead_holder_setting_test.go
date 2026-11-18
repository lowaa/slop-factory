package settings_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

func TestDeadHolderSetting_DefaultAndBounds(t *testing.T) {
	ctx := db.WithTenant(context.Background(), uuid.New())
	svc := stewardService(newFakeStore(), nil)

	got, err := svc.Get(ctx, settings.IntegrationLockDeadHolderAfter)
	require.NoError(t, err)
	assert.EqualValues(t, 60, got.Value.Int)

	for _, bad := range []int64{0, 4, 1441} {
		_, err := svc.Set(ctx, settings.IntegrationLockDeadHolderAfter, settings.IntValue(bad), actor)
		assert.ErrorIs(t, err, settings.ErrInvalidSetting, "%d", bad)
	}
	for _, ok := range []int64{5, 1440} {
		_, err := svc.Set(ctx, settings.IntegrationLockDeadHolderAfter, settings.IntValue(ok), actor)
		assert.NoError(t, err, "%d", ok)
	}
}

func TestPriorityAgingSetting_DefaultAndBounds(t *testing.T) {
	ctx := db.WithTenant(context.Background(), uuid.New())
	svc := stewardService(newFakeStore(), nil)

	got, err := svc.Get(ctx, settings.IntegrationLockPriorityAgingMinutes)
	require.NoError(t, err)
	assert.EqualValues(t, 30, got.Value.Int)

	for _, bad := range []int64{0, 4, 241} {
		_, err := svc.Set(ctx, settings.IntegrationLockPriorityAgingMinutes, settings.IntValue(bad), actor)
		assert.ErrorIs(t, err, settings.ErrInvalidSetting, "%d", bad)
	}
	for _, ok := range []int64{5, 240} {
		_, err := svc.Set(ctx, settings.IntegrationLockPriorityAgingMinutes, settings.IntValue(ok), actor)
		assert.NoError(t, err, "%d", ok)
	}
}

func TestMergeQueueBatchSizeSetting_DefaultOwnerOnlyAndBounds(t *testing.T) {
	ctx := db.WithTenant(context.Background(), uuid.New())
	svc := stewardService(newFakeStore(), &fakeOwners{owner: true})

	got, err := svc.Get(ctx, settings.MergeQueueBatchSize)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.Value.Int, "batching is off until an owner raises it")

	_, err = stewardService(newFakeStore(), &fakeOwners{owner: false}).Set(ctx, settings.MergeQueueBatchSize, settings.IntValue(2), actor)
	assert.ErrorIs(t, err, settings.ErrNotOwner)

	for _, bad := range []int64{0, -1, 4} {
		_, err := svc.Set(ctx, settings.MergeQueueBatchSize, settings.IntValue(bad), actor)
		assert.ErrorIs(t, err, settings.ErrInvalidSetting, "%d", bad)
	}
	for _, ok := range []int64{1, 2, 3} {
		_, err := svc.Set(ctx, settings.MergeQueueBatchSize, settings.IntValue(ok), actor)
		require.NoError(t, err, "%d", ok)
		got, err := svc.Get(ctx, settings.MergeQueueBatchSize)
		require.NoError(t, err)
		assert.EqualValues(t, ok, got.Value.Int)
	}
}

func TestWorkspaceReconcileSetting_OffByDefaultAndOwnerOnly(t *testing.T) {
	ctx := db.WithTenant(context.Background(), uuid.New())
	svc := stewardService(newFakeStore(), &fakeOwners{owner: true})

	on, err := svc.WorkspaceReconcileEnabled(ctx)
	require.NoError(t, err)
	assert.False(t, on)

	_, err = stewardService(newFakeStore(), &fakeOwners{owner: false}).Set(ctx, settings.WorkspaceReconcile, settings.BoolValue(true), actor)
	assert.ErrorIs(t, err, settings.ErrNotOwner)

	_, err = svc.Set(ctx, settings.WorkspaceReconcile, settings.BoolValue(true), actor)
	require.NoError(t, err)
	on, err = svc.WorkspaceReconcileEnabled(ctx)
	require.NoError(t, err)
	assert.True(t, on)
}
