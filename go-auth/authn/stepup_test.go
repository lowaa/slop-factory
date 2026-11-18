package authn_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/go-auth/authn"
)

func TestRequireRecentAuth(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		authTime time.Time
		ok       bool
	}{
		{"just now", now, true},
		{"inside the window", now.Add(-authn.StepUpMaxAge + time.Second), true},
		{"exactly at the edge", now.Add(-authn.StepUpMaxAge), true},
		{"past the window", now.Add(-authn.StepUpMaxAge - time.Second), false},
		{"no auth time recorded", time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := authn.RequireRecentAuthAt(&authn.User{AuthTime: c.authTime}, now)
			if c.ok {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, authn.ErrReauthRequired)
			}
		})
	}
}
