package steward_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

type recordingReader struct {
	calls  int
	values map[string]settings.Setting
}

func (r *recordingReader) GetMany(_ context.Context, names ...string) (map[string]settings.Setting, error) {
	r.calls++
	out := map[string]settings.Setting{}
	for _, n := range names {
		out[n] = r.values[n]
	}
	return out, nil
}

func TestTenantSettings_ReadsEveryKeyInOneCall(t *testing.T) {
	r := &recordingReader{values: map[string]settings.Setting{
		steward.SettingMode:          {Value: settings.StrValue("live")},
		steward.SettingCategoryTiers: {Value: settings.StrValue(`{}`)},
		steward.SettingDailyCap:      {Value: settings.IntValue(7)},
		steward.SettingVetoMinutes:   {Value: settings.IntValue(15)},

		steward.SettingWarnMinDecisions:    {Value: settings.IntValue(11)},
		steward.SettingWarnMinAgreementPct: {Value: settings.IntValue(88)},
	}}
	cfg, err := steward.NewTenantSettings(r).StewardConfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, r.calls)
	assert.Equal(t, steward.ModeLive, cfg.Mode)
	assert.Equal(t, 7, cfg.DailyCap)
	assert.Equal(t, 15, cfg.VetoMinutes)
	assert.Equal(t, 11, cfg.WarnMinDecisions)
	assert.Equal(t, 88, cfg.WarnMinAgreementPct)
}

func TestTenantSettings_RefusesAnUnparsableMode(t *testing.T) {
	r := &recordingReader{values: map[string]settings.Setting{steward.SettingMode: {Value: settings.StrValue("on")}}}
	_, err := steward.NewTenantSettings(r).StewardConfig(context.Background())
	assert.ErrorIs(t, err, steward.ErrInvalidSetting)
}

func TestTenantSettings_LatestOfLevelAndModeWins(t *testing.T) {
	t0 := time.Now()
	level := func(n int64, at time.Time) settings.Setting {
		return settings.Setting{Value: settings.IntValue(n), UpdatedAt: at}
	}
	mode := func(m string, at time.Time) settings.Setting {
		return settings.Setting{Value: settings.StrValue(m), UpdatedAt: at}
	}
	cases := []struct {
		name  string
		level settings.Setting
		mode  settings.Setting
		want  steward.Level
	}{
		{"level newer than mode", level(4, t0.Add(time.Hour)), mode("shadow", t0), steward.LevelDelegated},
		{"Off switch written after the level", level(4, t0), mode("off", t0.Add(time.Hour)), steward.LevelOff},
		{"explicit level 0 beats an older live mode", level(0, t0.Add(time.Hour)), mode("live", t0), steward.LevelOff},
		{"unwritten level falls back to live", settings.Setting{Value: settings.IntValue(-1)}, mode("live", t0), steward.LevelRoutine},
		{"unwritten level falls back to shadow", settings.Setting{Value: settings.IntValue(-1)}, mode("shadow", t0), steward.LevelShadow},
		{"nothing written", settings.Setting{Value: settings.IntValue(-1)}, settings.Setting{Value: settings.StrValue("off")}, steward.LevelOff},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &recordingReader{values: map[string]settings.Setting{steward.SettingLevel: c.level, steward.SettingMode: c.mode}}
			cfg, err := steward.NewTenantSettings(r).StewardConfig(context.Background())
			require.NoError(t, err)
			assert.Equal(t, c.want, cfg.Level)
			assert.Equal(t, c.want.Mode(), cfg.Mode)
		})
	}
}

func TestTenantSettings_ReviewMaxLines(t *testing.T) {
	read := func(n int64) (int, error) {
		r := &recordingReader{values: map[string]settings.Setting{steward.SettingReviewMaxLines: {Value: settings.IntValue(n)}}}
		return steward.NewTenantSettings(r).ReviewMaxLines(context.Background())
	}
	got, err := read(250)
	require.NoError(t, err)
	assert.Equal(t, 250, got)

	_, err = read(0)
	assert.ErrorIs(t, err, steward.ErrInvalidSetting, "a stored value the rules refuse fails the check rather than allowing everything")
}

func TestReviewMaxLinesSetting_DefaultsTo400AndIsOwnerOnly(t *testing.T) {
	for _, s := range steward.SettingSpecs() {
		if s.Definition.Name != steward.SettingReviewMaxLines {
			continue
		}
		assert.True(t, s.OwnerOnly)
		assert.EqualValues(t, 400, s.Definition.Default.Int)
		assert.Error(t, s.Validate(settings.IntValue(-1)))
		assert.NoError(t, s.Validate(settings.IntValue(1)))
		return
	}
	t.Fatal("setting is not declared")
}
