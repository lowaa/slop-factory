package consent_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/consent"
)

func sampleCookie(f *fixture) *consent.CookieState {
	return &consent.CookieState{
		Decisions: map[string]*consent.CookieDecision{
			"analytics": {Purpose: "analytics", Granted: true, Version: "v1", DecidedAt: f.clock.Now(), Source: consent.SourceBanner, Unmerged: true},
		},
		MergeToken: "tok",
		Expires:    f.clock.Now().Add(time.Hour),
	}
}

func TestCookieRoundTrip(t *testing.T) {
	f := newFixture(t)
	v, err := f.codec.Encode(sampleCookie(f))
	require.NoError(t, err)
	got, err := f.codec.Decode(v)
	require.NoError(t, err)
	assert.Equal(t, "tok", got.MergeToken)
	d := got.Decisions["analytics"]
	require.NotNil(t, d)
	assert.True(t, d.Granted && d.Unmerged)
	assert.True(t, d.DecidedAt.Equal(f.clock.Now()))
}

func TestCookieIsRefusedWhenNotSealedByThisServer(t *testing.T) {
	f := newFixture(t)
	v, err := f.codec.Encode(sampleCookie(f))
	require.NoError(t, err)

	other, err := consent.NewCookieCodec(&consent.CookieConfig{Name: "app_consent", Secret: []byte("another-secret-another-secret-00")})
	require.NoError(t, err)
	forged, err := other.Encode(sampleCookie(f))
	require.NoError(t, err)

	parts := strings.Split(v, ".")
	require.Len(t, parts, 3)
	// Flip the payload (a grant to a denial in spirit) keeping the MAC.
	tamperedPayload := parts[0] + "." + parts[1][:len(parts[1])-2] + "AA" + "." + parts[2]

	for name, value := range map[string]string{
		"another key":      forged,
		"tampered payload": tamperedPayload,
		"stripped mac":     parts[0] + "." + parts[1],
		"empty":            "",
		"garbage":          "not-a-cookie",
		"other version":    "v2." + parts[1] + "." + parts[2],
		"oversized":        strings.Repeat("a", 5000),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.codec.Decode(value)
			require.Error(t, err)
			if value != "" {
				assert.NotContains(t, err.Error(), value, "the error never echoes the value")
			}
			assert.Nil(t, f.codec.OpenValue(value, f.clock.Now()))
		})
	}
}

func TestCookieAttributes(t *testing.T) {
	f := newFixture(t)
	ck, err := f.codec.HTTPCookie(sampleCookie(f), f.clock.Now())
	require.NoError(t, err)
	assert.Equal(t, "app_consent", ck.Name)
	assert.True(t, ck.HttpOnly, "only the server reads it")
	assert.True(t, ck.Secure)
	assert.Equal(t, http.SameSiteLaxMode, ck.SameSite)
	assert.Equal(t, "/", ck.Path)
	assert.Equal(t, 3600, ck.MaxAge)
}

func TestCookieCodecConfig(t *testing.T) {
	_, err := consent.NewCookieCodec(&consent.CookieConfig{Name: "c", Secret: []byte("short")})
	assert.Error(t, err, "a weak secret is refused")
	_, err = consent.NewCookieCodec(&consent.CookieConfig{Secret: secret})
	assert.Error(t, err, "a name is required")
}

func TestRegistryValidation(t *testing.T) {
	ok := consent.Purpose{ID: "analytics", Kind: consent.CookieCategory, Document: "cookie_policy"}
	cases := map[string][]consent.Purpose{
		"none":                    {},
		"duplicate":               {ok, ok},
		"bad id":                  {{ID: "Analytics", Kind: consent.CookieCategory, Document: "d"}},
		"unknown kind":            {{ID: "x", Kind: "other", Document: "d"}},
		"no document":             {{ID: "x", Kind: consent.CookieCategory}},
		"gated necessary":         {{ID: "x", Kind: consent.CookieCategory, Necessary: true, Capability: "flag"}},
		"necessary processing":    {{ID: "x", Kind: consent.Processing, Necessary: true}},
		"necessary sale or share": {{ID: "x", Kind: consent.CookieCategory, Necessary: true, SaleOrShare: true}},
	}
	for name, ps := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := consent.NewRegistry(ps...)
			assert.Error(t, err)
		})
	}
	r, err := consent.NewRegistry(ok)
	require.NoError(t, err)
	assert.Equal(t, "analytics", r.Get("analytics").ID)
	assert.Nil(t, r.Get("nope"))
}
