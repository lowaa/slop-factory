package steward

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Mode is how far the steward's assessments go.
type Mode string

const (
	ModeOff    Mode = "off"
	ModeShadow Mode = "shadow"
	ModeLive   Mode = "live"
)

// Bounds and defaults of the numeric settings.
const (
	MinVetoMinutes     = 5
	MaxVetoMinutes     = 1440
	DefaultVetoMinutes = 60
	MaxDailyCap        = 10_000
	DefaultDailyCap    = 50

	// DefaultReviewMaxLines is the largest change, in hand-written changed
	// lines, whose review the steward may approve.
	DefaultReviewMaxLines = 400
	MaxReviewMaxLines     = 100_000

	DefaultWarnMinDecisions    = 20
	MaxWarnMinDecisions        = 10_000
	DefaultWarnMinAgreementPct = 95
)

// ErrInvalidSetting is a steward setting value the rules refuse.
var ErrInvalidSetting = errors.New("steward: invalid setting")

func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeOff, ModeShadow, ModeLive:
		return m, nil
	}
	return "", fmt.Errorf("%w: mode must be off, shadow or live", ErrInvalidSetting)
}

func ValidateWarnMinDecisions(n int64) error {
	if n < 0 || n > MaxWarnMinDecisions {
		return fmt.Errorf("%w: warning decision count must be 0..%d", ErrInvalidSetting, MaxWarnMinDecisions)
	}
	return nil
}

func ValidateWarnMinAgreementPct(n int64) error {
	if n < 0 || n > 100 {
		return fmt.Errorf("%w: warning agreement must be 0..100", ErrInvalidSetting)
	}
	return nil
}

func ValidateVetoMinutes(n int64) error {
	if n < MinVetoMinutes || n > MaxVetoMinutes {
		return fmt.Errorf("%w: veto minutes must be %d..%d", ErrInvalidSetting, MinVetoMinutes, MaxVetoMinutes)
	}
	return nil
}

func ValidateReviewMaxLines(n int64) error {
	if n < 1 || n > MaxReviewMaxLines {
		return fmt.Errorf("%w: review max lines must be 1..%d", ErrInvalidSetting, MaxReviewMaxLines)
	}
	return nil
}

func ValidateDailyCap(n int64) error {
	if n < 0 || n > MaxDailyCap {
		return fmt.Errorf("%w: daily cap must be 0..%d", ErrInvalidSetting, MaxDailyCap)
	}
	return nil
}

// TierOverrides is a tenant's per-category tier choice.
type TierOverrides map[string]Tier

// ParseTierOverrides reads the stored JSON object of category -> tier. It
// refuses an unknown category, an unknown tier, and a NeverAuto category
// raised above Human, so the settings write fails where the owner can see it.
func ParseTierOverrides(raw string) (TierOverrides, error) {
	if raw == "" {
		return TierOverrides{}, nil
	}
	var in map[string]Tier
	if err := json.Unmarshal([]byte(raw), &in); err != nil || in == nil {
		return nil, fmt.Errorf("%w: tiers must be a JSON object of category to tier", ErrInvalidSetting)
	}
	for id, t := range in {
		if Retired(id) {
			delete(in, id)
			continue
		}
		c, ok := Lookup(id)
		if !ok {
			return nil, fmt.Errorf("%w: unknown category %q", ErrInvalidSetting, id)
		}
		if !t.valid() {
			return nil, fmt.Errorf("%w: unknown tier %q for %s", ErrInvalidSetting, t, id)
		}
		if c.NeverAuto && t.autonomy() > TierHuman.autonomy() {
			return nil, fmt.Errorf("%w: %s is never automatic", ErrInvalidSetting, id)
		}
	}
	return TierOverrides(in), nil
}
