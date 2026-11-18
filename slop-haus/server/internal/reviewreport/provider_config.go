package reviewreport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gosettings "github.com/lowaa/slop-factory/go-settings"
	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

// ProviderConfigSetting is the tenant setting holding the provider config
// slop-review runs with (providerconfig.Settings, as JSON), or "" for none.
// A string rather than jsonb: go-settings has no jsonb type yet. Owner-only:
// it decides where every reviewed diff is sent.
const ProviderConfigSetting = "slop_review.provider_config"

// maxProviderConfigBytes is far more than any valid config needs, and well
// inside what the reviewer accepts.
const maxProviderConfigBytes = 8 << 10

// SettingSpecs declares the provider config setting, for settings.Register
// at wiring.
func SettingSpecs() []settings.Spec {
	return []settings.Spec{{
		Definition: gosettings.Definition{Name: ProviderConfigSetting, Default: settings.StrValue("")},
		OwnerOnly:  true,
		Validate: func(v settings.Value) error {
			if v.Str == "" {
				return nil
			}
			if _, err := parseProviderSettings(v.Str); err != nil {
				return fmt.Errorf("%w: %v", settings.ErrInvalidSetting, err)
			}
			return nil
		},
	}}
}

// parseProviderSettings reads a stored config. Unknown fields are refused: a
// person typed it, and a misspelt fallback must not be silently dropped.
func parseProviderSettings(raw string) (*providerconfig.Settings, error) {
	if len(raw) > maxProviderConfigBytes {
		return nil, fmt.Errorf("the provider config is over %d bytes", maxProviderConfigBytes)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var s providerconfig.Settings
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("the provider config is not a JSON object of primary, fallback and fallback_on: %w", err)
	}
	if dec.More() {
		return nil, errors.New("the provider config has more than one JSON value")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// SettingsHistory is the part of settings.Service the provider config is
// read through: its newest change, whose id is the config's revision.
type SettingsHistory interface {
	History(ctx context.Context, name string, before int64, limit int) ([]settings.Change, int64, error)
}

var errStoredConfigInvalid = errors.New("reviewreport: the stored provider config is not valid")

// readProviderConfig is the tenant's config, revisioned by the id of the
// setting's newest change. Ids only go up, so a reviewer's "never replace a
// newer saved copy with an older one" holds, though they are not a count of
// the tenant's saves.
func readProviderConfig(ctx context.Context, history SettingsHistory) (*providerconfig.Config, error) {
	changes, _, err := history.History(ctx, ProviderConfigSetting, 0, 1)
	if err != nil {
		return nil, fmt.Errorf("read the provider config: %w", err)
	}
	if len(changes) == 0 || changes[0].To.Type != settings.TypeStr || changes[0].To.Str == "" {
		return nil, ErrNoProviderConfig
	}
	latest := changes[0]
	s, err := parseProviderSettings(latest.To.Str)
	if err != nil {
		return nil, errors.Join(errStoredConfigInvalid, err)
	}
	cfg := &providerconfig.Config{Settings: *s, Revision: latest.ID}
	if err := cfg.Validate(); err != nil {
		return nil, errors.Join(errStoredConfigInvalid, err)
	}
	return cfg, nil
}
