package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The policy's values are checked where the services are built from them
// (cmd/api's policy_test.go); what is left here is what comes from settings.

func TestSignInLimitsTakeTheClientCountsFromSettings(t *testing.T) {
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, 30, cfg.SignInLimits().LoginClient.Limit, "LOGIN_CLIENT_LIMIT's default")
	assert.Equal(t, 20, cfg.SignInLimits().MailClient.Limit, "MAIL_CLIENT_LIMIT's default")

	flags := valid()
	flags["login-client-limit"] = "7"
	flags["mail-client-limit"] = "9"
	cfg, err = load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, 7, cfg.SignInLimits().LoginClient.Limit)
	assert.Equal(t, 9, cfg.SignInLimits().MailClient.Limit)
}
