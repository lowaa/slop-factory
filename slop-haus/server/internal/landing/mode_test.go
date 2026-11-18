package landing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

// settingFake returns the one value it is given, and records the names asked.
type settingFake struct {
	value string
	err   error
	asked []string
}

func (s *settingFake) Get(_ context.Context, name string) (settings.Setting, error) {
	s.asked = append(s.asked, name)
	return settings.Setting{Name: name, Value: settings.StrValue(s.value)}, s.err
}

func TestSettingsMode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		method  string
		enabled bool
		want    bool
	}{
		{"queue and switch on", landing.MergeMethodQueue, true, true},
		{"queue but kill switch off", landing.MergeMethodQueue, false, false},
		{"worker", landing.MergeMethodWorker, true, false},
		{"worker and switch off", landing.MergeMethodWorker, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := landing.NewSettingsMode(&settingFake{value: tc.method}, tc.enabled).Enabled(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSettingsMode_KillSwitchOffReadsNoSetting(t *testing.T) {
	s := &settingFake{value: landing.MergeMethodQueue}
	_, err := landing.NewSettingsMode(s, false).Enabled(context.Background())
	require.NoError(t, err)
	assert.Empty(t, s.asked)
}

func TestSettingsMode_AReadErrorIsNotServed(t *testing.T) {
	on, err := landing.NewSettingsMode(&settingFake{err: errors.New("db down")}, true).Enabled(context.Background())
	assert.Error(t, err)
	assert.False(t, on)
}

func TestSettingSpecs_MergeMethodIsOwnerOnlyAndValidated(t *testing.T) {
	specs := landing.SettingSpecs()
	require.Len(t, specs, 1)
	spec := specs[0]
	assert.Equal(t, landing.SettingMergeMethod, spec.Definition.Name)
	assert.True(t, spec.OwnerOnly)
	assert.Equal(t, landing.MergeMethodWorker, spec.Definition.Default.Str, "off until an owner turns it on")
	assert.NoError(t, spec.Validate(settings.StrValue(landing.MergeMethodQueue)))
	assert.NoError(t, spec.Validate(settings.StrValue(landing.MergeMethodWorker)))
	assert.Error(t, spec.Validate(settings.StrValue("squash")))
}
