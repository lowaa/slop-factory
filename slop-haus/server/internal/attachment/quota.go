package attachment

import (
	"context"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"

	gosettings "github.com/lowaa/slop-factory/go-settings"
)

// QuotaSetting is the tenant setting holding the attachment quota, in bytes.
// Owner-only: it bounds what the tenant can store.
const QuotaSetting = "attachment_quota_bytes"

const maxQuotaBytes int64 = 1 << 40

// SettingSpecs declares the quota setting, for settings.Register at wiring.
func SettingSpecs() []settings.Spec {
	return []settings.Spec{{
		Definition: gosettings.Definition{Name: QuotaSetting, Default: settings.IntValue(DefaultQuotaBytes)},
		OwnerOnly:  true,
		Validate: func(v settings.Value) error {
			if v.Int < 0 || v.Int > maxQuotaBytes {
				return fmt.Errorf("%w: the attachment quota must be 0..%d bytes", settings.ErrInvalidSetting, maxQuotaBytes)
			}
			return nil
		},
	}}
}

// QuotaSource reads the context's tenant's quota.
type QuotaSource interface {
	Quota(ctx context.Context) (int64, error)
}

// SettingsQuota reads the quota from the tenant's settings.
type SettingsQuota struct {
	Settings interface {
		Get(ctx context.Context, name string) (settings.Setting, error)
	}
}

func (q SettingsQuota) Quota(ctx context.Context) (int64, error) {
	s, err := q.Settings.Get(ctx, QuotaSetting)
	if err != nil {
		return 0, fmt.Errorf("attachment: read the quota: %w", err)
	}
	return s.Value.Int, nil
}
