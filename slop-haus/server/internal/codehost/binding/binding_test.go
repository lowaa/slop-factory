package binding

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalize(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		kind     Kind
	}{
		{"github.com", "github.com", KindHost},
		{" GitHub.com/Acme ", "github.com/acme", KindOwner},
		{"GitHub.com/Acme/Repo", "github.com/acme/repo", KindRepo},
	} {
		got, kind, err := Normalize(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got)
		assert.Equal(t, tc.kind, kind)
	}
}

func TestNormalizeInvalid(t *testing.T) {
	for _, in := range []string{
		"", " ", "/", "github.com/", "/acme", "github.com//repo", "github.com/a/b/c",
		"github.com/a b", "github.com/../x", "https://github.com/a", "github.com/a/b/",
		"user@github.com", "github.com/a?x",
	} {
		_, _, err := Normalize(in)
		assert.ErrorIs(t, err, ErrInvalid, "%q", in)
	}
}

func TestResolve(t *testing.T) {
	all := []string{"github.com", "github.com/acme", "github.com/acme/api"}
	for _, tc := range []struct {
		name     string
		bindings []string
		repo     string
		want     string
		ok       bool
	}{
		{"repo beats owner and host", all, "github.com/acme/api", "github.com/acme/api", true},
		{"owner beats host", all, "github.com/acme/web", "github.com/acme", true},
		{"host fallback", all, "github.com/other/x", "github.com", true},
		{"order independent", []string{"github.com/acme/api", "github.com", "github.com/acme"}, "github.com/acme/api", "github.com/acme/api", true},
		{"segment not string prefix", []string{"github.com/acme"}, "github.com/acme-corp/x", "", false},
		{"repo not string prefix", []string{"github.com/acme/api"}, "github.com/acme/api-2", "", false},
		{"host not string prefix", []string{"github.com"}, "github.community/a/b", "", false},
		{"case insensitive", []string{"GitHub.com/ACME"}, "github.com/Acme/Web", "github.com/acme", true},
		{"no match", []string{"gitlab.com"}, "github.com/acme/api", "", false},
		{"no bindings", nil, "github.com/acme/api", "", false},
		{"invalid bindings skipped", []string{"", "github.com/a/b/c", "github.com"}, "github.com/acme/api", "github.com", true},
		{"repo must be full remote", []string{"github.com"}, "github.com/acme", "", false},
		{"invalid repo", []string{"github.com"}, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Resolve(tc.bindings, tc.repo)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestKindString(t *testing.T) {
	assert.Equal(t, "host", KindHost.String())
	assert.Equal(t, "owner", KindOwner.String())
	assert.Equal(t, "repo", KindRepo.String())
	assert.Equal(t, "invalid", Kind(0).String())
}
