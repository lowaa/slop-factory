package settings_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

type fakeOwners struct{ owner bool }

func (f fakeOwners) IsTenantOwner(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return f.owner, nil
}

func stewardService(store settings.Store, owner *fakeOwners) *settings.Service {
	var opts []settings.Option
	if owner != nil {
		opts = append(opts, settings.WithOwners(owner))
	}
	return settings.NewService(store, audit.New(slog.DiscardHandler, nil), slog.DiscardHandler, opts...)
}

func TestStewardSettings_Defaults(t *testing.T) {
	svc := stewardService(newFakeStore(), nil)
	ctx := context.Background()
	mode, err := svc.Get(ctx, steward.SettingMode)
	require.NoError(t, err)
	assert.Equal(t, string(steward.ModeOff), mode.Value.Str)
	veto, _ := svc.Get(ctx, steward.SettingVetoMinutes)
	assert.EqualValues(t, 60, veto.Value.Int)
	cap, _ := svc.Get(ctx, steward.SettingDailyCap)
	assert.EqualValues(t, 50, cap.Value.Int)
	tiers, _ := svc.Get(ctx, steward.SettingCategoryTiers)
	assert.Equal(t, "{}", tiers.Value.Str)
}

func TestStewardSettings_OwnerOnly(t *testing.T) {
	ctx := db.WithTenant(context.Background(), uuid.New())
	v := settings.StrValue("shadow")

	for name, owners := range map[string]*fakeOwners{"not an owner": {owner: false}, "no owner check configured": nil} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			_, err := stewardService(store, owners).Set(ctx, steward.SettingMode, v, actor)
			assert.ErrorIs(t, err, settings.ErrNotOwner)
			assert.Zero(t, store.upserts)
		})
	}

	store := newFakeStore()
	_, err := stewardService(store, &fakeOwners{owner: true}).Set(ctx, steward.SettingMode, v, actor)
	require.NoError(t, err)
	assert.Equal(t, 1, store.upserts)
}

func TestStewardSettings_OtherSettingsNeedNoOwner(t *testing.T) {
	_, err := stewardService(newFakeStore(), nil).Set(context.Background(), settings.TicketProcessingEnabled, settings.BoolValue(false), actor)
	assert.NoError(t, err)
}

func TestStewardSettings_Validation(t *testing.T) {
	ctx := db.WithTenant(context.Background(), uuid.New())
	svc := stewardService(newFakeStore(), &fakeOwners{owner: true})
	cases := map[string]settings.Value{
		steward.SettingMode:          settings.StrValue("on"),
		steward.SettingVetoMinutes:   settings.IntValue(4),
		steward.SettingDailyCap:      settings.IntValue(-1),
		steward.SettingCategoryTiers: settings.StrValue(`{"nope":"auto"}`),
	}
	for name, v := range cases {
		_, err := svc.Set(ctx, name, v, actor)
		assert.ErrorIs(t, err, steward.ErrInvalidSetting, name)
		assert.ErrorIs(t, err, settings.ErrInvalidSetting, name)
	}
	_, err := svc.Set(ctx, steward.SettingCategoryTiers, settings.StrValue(`{"priority":"auto"}`), actor)
	assert.NoError(t, err)
}

func TestGetMany_StoredAndDefaultedAndUnknown(t *testing.T) {
	ctx := db.WithTenant(context.Background(), uuid.New())
	svc := stewardService(newFakeStore(), &fakeOwners{owner: true})
	_, err := svc.Set(ctx, steward.SettingDailyCap, settings.IntValue(9), actor)
	require.NoError(t, err)

	got, err := svc.GetMany(ctx, steward.SettingDailyCap, steward.SettingVetoMinutes)
	require.NoError(t, err)
	assert.EqualValues(t, 9, got[steward.SettingDailyCap].Value.Int)
	assert.EqualValues(t, 60, got[steward.SettingVetoMinutes].Value.Int)

	_, err = svc.GetMany(ctx, steward.SettingDailyCap, "nope")
	assert.ErrorIs(t, err, settings.ErrUnknownSetting)
}
