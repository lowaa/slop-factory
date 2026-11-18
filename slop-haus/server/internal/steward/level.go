package steward

import "fmt"

// Level is the tenant's autonomy level, 0 to 5. It sets every category's
// default tier by the category's Class, and derives the engine's Mode, so the
// owner reasons about one setting.
type Level int

const (
	LevelOff     Level = 0
	LevelShadow  Level = 1
	LevelAssist  Level = 2
	LevelRoutine Level = 3
	// LevelDelegated adds the judgment categories at veto.
	LevelDelegated Level = 4
	// LevelFull adds the judgment categories at auto.
	LevelFull Level = 5

	MaxLevel = LevelFull

	// levelUnset is the stored default of steward_level: the owner has not
	// chosen, so the deprecated steward_mode decides. It is not 0 because the
	// settings store drops a first write equal to the default.
	levelUnset = -1
)

// Class groups categories that share a default tier at each level. It is code,
// versioned with the checks, so the owner cannot widen a category's envelope
// by relabelling it.
type Class string

const (
	ClassMechanical   Class = "mechanical"
	ClassHousekeeping Class = "housekeeping"
	ClassJudgment     Class = "judgment"
)

func ValidateLevel(n int64) error {
	if n < int64(LevelOff) || n > int64(MaxLevel) {
		return fmt.Errorf("%w: level must be %d..%d", ErrInvalidSetting, LevelOff, MaxLevel)
	}
	return nil
}

// Mode is the engine's switch derived from the level. Level 2 only adds
// drafts, which are not the engine's concern, so it assesses like shadow.
func (l Level) Mode() Mode {
	switch {
	case l <= LevelOff:
		return ModeOff
	case l < LevelRoutine:
		return ModeShadow
	default:
		return ModeLive
	}
}

// assessedAs is the level whose tiers a non-applying level records as its
// hypothetical verdict. Shadow and assist assess what the live steward would
// do, so the agreement record has something to compare with the owner's real
// decision; nothing is applied because Mode is not live.
func (l Level) assessedAs() Level {
	if l > LevelOff && !l.Applies() {
		return LevelRoutine
	}
	return l
}

// Applies reports whether the level lets the steward apply anything. Below
// it, no tier or override yields veto or auto.
func (l Level) Applies() bool { return l >= LevelRoutine }

// LevelOfMode is the level a stored steward_mode stands for, while that
// setting is still read as a fallback.
func LevelOfMode(m Mode) Level {
	switch m {
	case ModeShadow:
		return LevelShadow
	case ModeLive:
		return LevelRoutine
	default:
		return LevelOff
	}
}

// DefaultTier is the tier the level gives a class.
func (l Level) DefaultTier(c Class) Tier {
	if !l.Applies() {
		return TierHuman
	}
	switch c {
	case ClassMechanical:
		return TierAuto
	case ClassHousekeeping:
		// Level 5 leaves housekeeping at veto (owner, 26 Oct).
		return TierVeto
	case ClassJudgment:
		switch l {
		case LevelDelegated:
			return TierVeto
		case LevelFull:
			return TierAuto
		}
	}
	return TierHuman
}
