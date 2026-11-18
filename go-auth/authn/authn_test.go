package authn_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/go-auth/authn"
)

func TestBearerToken(t *testing.T) {
	for header, want := range map[string]string{
		"Bearer good-token":     "good-token",
		"bearer good-token":     "good-token",
		"BEARER   good-token  ": "good-token",
		"Bearer":                "",
		"Bearergood-token":      "",
		"Bearer ":               "",
		"Bearer    ":            "",
		"Bearer\tgood-token":    "",
		"Bearer good token":     "good token",
		"Basic dXNlcjpwYXNz":    "",
		"":                      "",
	} {
		assert.Equal(t, want, authn.BearerToken(header), "%q", header)
	}
}
