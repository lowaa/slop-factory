package githubapp

import (
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

const (
	// appJWTLifetime is under GitHub's ten-minute ceiling, with room for a
	// clock that is a little ahead of GitHub's.
	appJWTLifetime = 9 * time.Minute
	// appJWTBackdate allows for a clock that is a little behind it.
	appJWTBackdate = time.Minute
)

// appJWT is the short-lived RS256 token that authenticates slop-haus as the
// App itself. It can mint installation tokens and read the installation,
// and is sent to nothing but the configured GitHub API (see Client).
type appJWT struct {
	secretbox.Redacted
	value string
}

func (j appJWT) bearer() string { return j.value }

func signAppJWT(key *PrivateKey, appID int64, now time.Time) (appJWT, error) {
	claims := jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(appID, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-appJWTBackdate)),
		ExpiresAt: jwt.NewNumericDate(now.Add(appJWTLifetime)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key.key)
	if err != nil {
		return appJWT{}, fmt.Errorf("githubapp: sign app token: %w", err)
	}
	return appJWT{value: signed}, nil
}
