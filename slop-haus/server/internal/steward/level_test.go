package steward_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

func TestEffectiveTier_LevelClassOverride(t *testing.T) {
	cat := func(class steward.Class) steward.Category {
		return steward.Category{ID: "c", Class: class}
	}
	pin := func(t steward.Tier) steward.TierOverrides { return steward.TierOverrides{"c": t} }
	H, V, A := steward.TierHuman, steward.TierVeto, steward.TierAuto
	M, K, J := steward.ClassMechanical, steward.ClassHousekeeping, steward.ClassJudgment

	cases := []struct {
		name  string
		level steward.Level
		class steward.Class
		ov    steward.TierOverrides
		want  steward.Tier
	}{
		{"0 mechanical", 0, M, nil, H},
		{"1 mechanical", 1, M, nil, H},
		{"2 housekeeping", 2, K, nil, H},
		{"2 judgment", 2, J, nil, H},
		{"3 mechanical", 3, M, nil, A},
		{"3 housekeeping", 3, K, nil, V},
		{"3 judgment", 3, J, nil, H},
		{"4 mechanical", 4, M, nil, A},
		{"4 housekeeping", 4, K, nil, V},
		{"4 judgment", 4, J, nil, V},
		{"5 mechanical", 5, M, nil, A},
		{"5 housekeeping stays veto", 5, K, nil, V},
		{"5 judgment", 5, J, nil, A},

		{"override lifts at 3", 3, K, pin(A), A},
		{"override lowers at 5", 5, J, pin(H), H},
		{"override lowers mechanical", 3, M, pin(V), V},
		{"override inert at 2", 2, K, pin(A), H},
		{"override inert at 1", 1, M, pin(V), H},
		{"override inert at 0", 0, M, pin(A), H},
		{"invalid override ignored", 3, M, pin("yolo"), A},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, steward.EffectiveTier(cat(c.class), c.level, c.ov))
		})
	}
}

func TestEffectiveTier_NeverAutoBeatsEveryLevelAndOverride(t *testing.T) {
	c := steward.Category{ID: "c", Class: steward.ClassMechanical, NeverAuto: true}
	for l := steward.LevelOff; l <= steward.MaxLevel; l++ {
		assert.Equal(t, steward.TierHuman, steward.EffectiveTier(c, l, steward.TierOverrides{"c": steward.TierAuto}), l)
	}
}

func TestLevelMode(t *testing.T) {
	want := []steward.Mode{steward.ModeOff, steward.ModeShadow, steward.ModeShadow, steward.ModeLive, steward.ModeLive, steward.ModeLive}
	for l, m := range want {
		assert.Equal(t, m, steward.Level(l).Mode(), l)
	}
}

func TestLevelOfModeMigration(t *testing.T) {
	assert.Equal(t, steward.LevelOff, steward.LevelOfMode(steward.ModeOff))
	assert.Equal(t, steward.LevelShadow, steward.LevelOfMode(steward.ModeShadow))
	assert.Equal(t, steward.LevelRoutine, steward.LevelOfMode(steward.ModeLive))
}

func TestValidateLevel(t *testing.T) {
	assert.NoError(t, steward.ValidateLevel(0))
	assert.NoError(t, steward.ValidateLevel(5))
	assert.ErrorIs(t, steward.ValidateLevel(-1), steward.ErrInvalidSetting)
	assert.ErrorIs(t, steward.ValidateLevel(6), steward.ErrInvalidSetting)
}

func TestEveryCategoryHasAClass(t *testing.T) {
	for _, c := range steward.Categories() {
		assert.Contains(t, []steward.Class{steward.ClassMechanical, steward.ClassHousekeeping, steward.ClassJudgment}, c.Class, c.ID)
	}
}
