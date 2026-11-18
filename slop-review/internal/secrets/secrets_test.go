package secrets

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLooksLikeSecret(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{".env", true},
		{".env.production", true},
		{"config/.env", true},
		{"server.pem", true},
		{"private.key", true},
		{"id_rsa", true},
		{"id_rsa.pub", true},
		{".npmrc", true},
		{".netrc", true},
		{"aws_credentials.json", true},
		{"credentials.json", true},
		{"main.go", false},
		{"secrets_test.go", false},
		{"README.md", false},
		{"keyboard_shortcuts.md", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			assert.Equal(t, c.want, LooksLikeSecret(c.path))
		})
	}
}
