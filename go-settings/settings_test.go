package settings_test

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	settings "github.com/lowaa/slop-factory/go-settings"
)

func TestRegistry_NamesAreSorted(t *testing.T) {
	reg, err := settings.NewRegistry(
		settings.Definition{Name: "z", Default: settings.BoolValue(false)},
		settings.Definition{Name: "a", Default: settings.BoolValue(false)})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "z"}, reg.Names())
}

func TestRegistry_ChecksNamesAndTypes(t *testing.T) {
	reg, err := settings.NewRegistry(settings.Definition{Name: "a", Default: settings.BoolValue(false)})
	require.NoError(t, err)

	assert.NoError(t, reg.Check("a", settings.BoolValue(true)))
	assert.ErrorIs(t, reg.Check("nope", settings.BoolValue(true)), settings.ErrUnknownSetting)
	assert.ErrorIs(t, reg.Check("a", settings.StrValue("x")), settings.ErrTypeMismatch)

	_, err = settings.NewRegistry(settings.Definition{Name: ""})
	assert.Error(t, err)
	_, err = settings.NewRegistry(settings.Definition{Name: "a", Default: settings.BoolValue(false)}, settings.Definition{Name: "a", Default: settings.BoolValue(true)})
	assert.Error(t, err)
	_, err = settings.NewRegistry(settings.Definition{Name: "a", Default: settings.Value{Type: "jsonb"}})
	assert.Error(t, err)
}

func TestRegistry_ValidateHookRefusesAValueOfTheRightType(t *testing.T) {
	errBadZone := errors.New("not a zone")
	reg, err := settings.NewRegistry(
		settings.Definition{
			Name:    "timezone",
			Default: settings.StrValue("UTC"),
			Validate: func(v settings.Value) error {
				if v.Str != "UTC" {
					return errBadZone
				}
				return nil
			},
		},
		settings.Definition{Name: "free", Default: settings.StrValue("")})
	require.NoError(t, err)

	assert.NoError(t, reg.Check("timezone", settings.StrValue("UTC")))
	err = reg.Check("timezone", settings.StrValue("Mars/Base"))
	assert.ErrorIs(t, err, settings.ErrInvalidValue)
	assert.ErrorIs(t, err, errBadZone, "the hook's own error stays inspectable")
	assert.NoError(t, reg.Check("free", settings.StrValue("Mars/Base")), "only the setting with a hook is held to it")
}

func TestRegistry_ValidateHookNeverSeesAWrongType(t *testing.T) {
	reg, err := settings.NewRegistry(settings.Definition{
		Name:    "n",
		Default: settings.IntValue(1),
		Validate: func(v settings.Value) error {
			if v.Type != settings.TypeInt {
				t.Fatal("the hook ran on a value of the wrong type")
			}
			return nil
		},
	})
	require.NoError(t, err)
	assert.ErrorIs(t, reg.Check("n", settings.StrValue("x")), settings.ErrTypeMismatch)
}

func TestValue_SameComparesTypeAndLiveField(t *testing.T) {
	assert.True(t, settings.BoolValue(true).Same(settings.BoolValue(true)))
	assert.False(t, settings.BoolValue(true).Same(settings.BoolValue(false)))
	assert.False(t, settings.IntValue(1).Same(settings.BoolValue(true)))
	assert.True(t, settings.Value{Type: settings.TypeStr, Str: "x", Int: 9}.Same(settings.StrValue("x")), "unused fields are ignored")
	assert.False(t, settings.FloatValue(1.5).Same(settings.FloatValue(2)))
	assert.True(t, settings.FloatValue(1.5).Same(settings.FloatValue(1.5)))
	assert.True(t, settings.FloatValue(math.NaN()).Same(settings.FloatValue(math.NaN())))
	assert.True(t, settings.IntValue(3).Same(settings.IntValue(3)))
	assert.False(t, settings.IntValue(3).Same(settings.IntValue(4)))
	assert.False(t, settings.StrValue("a").Same(settings.StrValue("b")))
	assert.False(t, settings.Value{Type: "jsonb"}.Same(settings.Value{Type: "jsonb"}))
}

func TestRegistry_RefusesADefaultItsOwnValidationRefuses(t *testing.T) {
	_, err := settings.NewRegistry(settings.Definition{
		Name:     "timezone",
		Default:  settings.StrValue("nowhere"),
		Validate: func(settings.Value) error { return errors.New("not a zone") },
	})
	assert.Error(t, err)
}
