package steward

import (
	"context"
	"fmt"

	gosettings "github.com/lowaa/slop-factory/go-settings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

// The steward's tenant settings. Owner-only to change: they decide which of
// the owner's approvals an agent may take.
const (
	// SettingLevel is the autonomy level, 0 (off) to 5 (full delegation). It
	// replaces SettingMode.
	SettingLevel = "steward_level"
	// SettingMode is deprecated: read as a fallback while SettingLevel has
	// never been written, then removed. off, shadow, shadow (assess and label, apply nothing) or live.
	SettingMode = "steward_mode"
	// SettingWarnMinDecisions and SettingWarnMinAgreementPct are the owner's
	// thresholds for the warnings shown when raising a category above human:
	// fewer decisions than N, or agreement below X%, is flagged. Advice only;
	// nothing blocks saving.
	SettingWarnMinDecisions    = "steward_warn_min_decisions"
	SettingWarnMinAgreementPct = "steward_warn_min_agreement_pct"
	// SettingVetoMinutes is how long a Veto-tier decision waits for the owner.
	SettingVetoMinutes = "steward_veto_minutes"
	// SettingDailyCap bounds the decisions the steward takes in a day.
	SettingDailyCap = "steward_daily_cap"
	// SettingReviewMaxLines is the largest change, in changed lines of
	// hand-written files, whose review the steward may approve.
	SettingReviewMaxLines = "steward_review_max_lines"
	// SettingCategoryTiers is a JSON object of category id to tier. A
	// never-automatic category cannot be raised.
	SettingCategoryTiers = "steward_category_tiers"
)

// SettingSpecs declares the steward's settings, for settings.Register at wiring.
func SettingSpecs() []settings.Spec {
	def := func(name string, d settings.Value) gosettings.Definition {
		return gosettings.Definition{Name: name, Default: d}
	}
	return []settings.Spec{
		{Definition: def(SettingLevel, settings.IntValue(levelUnset)), OwnerOnly: true,
			Validate: func(v settings.Value) error { return ValidateLevel(v.Int) }},
		{Definition: def(SettingWarnMinDecisions, settings.IntValue(DefaultWarnMinDecisions)), OwnerOnly: true,
			Validate: func(v settings.Value) error { return ValidateWarnMinDecisions(v.Int) }},
		{Definition: def(SettingWarnMinAgreementPct, settings.IntValue(DefaultWarnMinAgreementPct)), OwnerOnly: true,
			Validate: func(v settings.Value) error { return ValidateWarnMinAgreementPct(v.Int) }},
		{Definition: def(SettingMode, settings.StrValue(string(ModeOff))), OwnerOnly: true,
			Validate: func(v settings.Value) error { _, err := ParseMode(v.Str); return err }},
		{Definition: def(SettingVetoMinutes, settings.IntValue(DefaultVetoMinutes)), OwnerOnly: true,
			Validate: func(v settings.Value) error { return ValidateVetoMinutes(v.Int) }},
		{Definition: def(SettingDailyCap, settings.IntValue(DefaultDailyCap)), OwnerOnly: true,
			Validate: func(v settings.Value) error { return ValidateDailyCap(v.Int) }},
		{Definition: def(SettingReviewMaxLines, settings.IntValue(DefaultReviewMaxLines)), OwnerOnly: true,
			Validate: func(v settings.Value) error { return ValidateReviewMaxLines(v.Int) }},
		{Definition: def(SettingCategoryTiers, settings.StrValue("{}")), OwnerOnly: true,
			Validate: func(v settings.Value) error { _, err := ParseTierOverrides(v.Str); return err }},
	}
}

// SettingsReader reads several tenant settings in one go. Satisfied by *settings.Service.
type SettingsReader interface {
	GetMany(ctx context.Context, names ...string) (map[string]settings.Setting, error)
}

// TenantSettings reads the steward's Config from the tenant settings.
type TenantSettings struct{ reader SettingsReader }

func NewTenantSettings(r SettingsReader) *TenantSettings { return &TenantSettings{reader: r} }

var _ Settings = (*TenantSettings)(nil)

// StewardConfig reads every steward setting once.
func (t *TenantSettings) StewardConfig(ctx context.Context) (*Config, error) {
	got, err := t.reader.GetMany(ctx, SettingLevel, SettingMode, SettingCategoryTiers, SettingDailyCap, SettingVetoMinutes, SettingWarnMinDecisions, SettingWarnMinAgreementPct)
	if err != nil {
		return nil, err
	}
	level, err := levelFrom(got[SettingLevel], got[SettingMode])
	if err != nil {
		return nil, err
	}
	overrides, err := ParseTierOverrides(got[SettingCategoryTiers].Value.Str)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", SettingCategoryTiers, err)
	}
	return &Config{
		Level: level, Mode: level.Mode(), Tiers: overrides,
		DailyCap:            int(got[SettingDailyCap].Value.Int),
		VetoMinutes:         int(got[SettingVetoMinutes].Value.Int),
		WarnMinDecisions:    int(got[SettingWarnMinDecisions].Value.Int),
		WarnMinAgreementPct: int(got[SettingWarnMinAgreementPct].Value.Int),
	}, nil
}

// levelFrom gives the level of whichever of steward_level and the deprecated
// steward_mode was written last, so the owner's Off switch (which still writes
// the mode) keeps working after a level has been saved, and a level saved later
// supersedes an older mode. A level that was never written is unset (negative),
// not 0, so an explicit first write of 0 is stored and wins over a legacy
// "live" mode.
func levelFrom(level, mode settings.Setting) (Level, error) {
	levelSet := level.Value.Int >= 0 && !level.IsDefault()
	if levelSet && !mode.UpdatedAt.After(level.UpdatedAt) {
		if err := ValidateLevel(level.Value.Int); err != nil {
			return 0, fmt.Errorf("%s: %w", SettingLevel, err)
		}
		return Level(level.Value.Int), nil
	}
	m, err := ParseMode(mode.Value.Str)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", SettingMode, err)
	}
	return LevelOfMode(m), nil
}

var _ ReviewLimitReader = (*TenantSettings)(nil)

// ReviewMaxLines is the tenant's limit on a change the steward may approve.
func (t *TenantSettings) ReviewMaxLines(ctx context.Context) (int, error) {
	got, err := t.reader.GetMany(ctx, SettingReviewMaxLines)
	if err != nil {
		return 0, err
	}
	n := got[SettingReviewMaxLines].Value.Int
	if err := ValidateReviewMaxLines(n); err != nil {
		return 0, fmt.Errorf("%s: %w", SettingReviewMaxLines, err)
	}
	return int(n), nil
}
