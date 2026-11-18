package landing

import (
	"context"
	"fmt"

	gosettings "github.com/lowaa/slop-factory/go-settings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

// SettingMergeMethod is the tenant's choice of who lands a finished ticket.
const SettingMergeMethod = "merge_method"

const (
	// MergeMethodWorker leaves the merge to the author's agent, which takes the
	// integration lock itself.
	MergeMethodWorker = "worker"
	// MergeMethodQueue has the server land it from the landing queue.
	MergeMethodQueue = "merge_queue"
)

// SettingSpecs declares the landing settings, for settings.Register at wiring.
// The method is the owner's: it decides whether the server merges for the
// tenant.
func SettingSpecs() []settings.Spec {
	return []settings.Spec{{
		Definition: gosettings.Definition{Name: SettingMergeMethod, Default: settings.StrValue(MergeMethodWorker)},
		OwnerOnly:  true,
		Validate: func(v settings.Value) error {
			if v.Str != MergeMethodWorker && v.Str != MergeMethodQueue {
				return fmt.Errorf("merge method must be %q or %q", MergeMethodWorker, MergeMethodQueue)
			}
			return nil
		},
	}}
}

// SettingReader reads one tenant setting; *settings.Service is one.
type SettingReader interface {
	Get(ctx context.Context, name string) (settings.Setting, error)
}

// SettingsMode is Mode over the tenant's merge_method setting and the
// server-wide kill switch. The switch is checked first, so that off, no tenant
// setting is read.
type SettingsMode struct {
	settings     SettingReader
	killSwitchOn bool
}

// NewSettingsMode serves a tenant whose merge_method is merge_queue, while
// enabled (MERGE_QUEUE_ENABLED) is true.
func NewSettingsMode(s SettingReader, enabled bool) *SettingsMode {
	return &SettingsMode{settings: s, killSwitchOn: enabled}
}

func (m *SettingsMode) Enabled(ctx context.Context) (bool, error) {
	if !m.killSwitchOn {
		return false, nil
	}
	s, err := m.settings.Get(ctx, SettingMergeMethod)
	if err != nil {
		return false, err
	}
	return s.Value.Str == MergeMethodQueue, nil
}

var _ Mode = (*SettingsMode)(nil)
