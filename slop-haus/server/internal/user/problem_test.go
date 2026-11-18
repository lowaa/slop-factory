package user_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goauthn "github.com/lowaa/slop-factory/go-auth/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

func TestProblemFor_DetailLosesPackagePrefix(t *testing.T) {
	p := user.ProblemFor(fmt.Errorf("handler: %w", user.ErrNotSignedIn))

	require.NotNil(t, p)
	assert.Equal(t, http.StatusUnauthorized, p.Status)
	assert.Equal(t, "not_signed_in", p.Code)
	assert.Equal(t, "sign in first", p.Detail)
}

func TestProblemFor_ReauthRequiredIsGoAuths(t *testing.T) {
	p := user.ProblemFor(goauthn.ErrReauthRequired)

	require.NotNil(t, p)
	assert.Equal(t, http.StatusForbidden, p.Status)
	assert.Equal(t, "reauth_required", p.Code)
	assert.Equal(t, goauthn.ErrReauthRequired.Error(), p.Detail)
}

func TestProblemFor_EveryDetailIsFreeOfThePackagePrefix(t *testing.T) {
	for _, err := range []error{user.ErrInvalidCredentials, user.ErrNotFound, user.ErrRegistrationClosed, user.ErrLastMethod, user.ErrPostRequired} {
		p := user.ProblemFor(err)
		require.NotNil(t, p, err)
		assert.NotContains(t, p.Detail, "user: ")
	}
}
