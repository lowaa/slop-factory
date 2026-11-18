package logpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLogger(t *testing.T, cfg *Config) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	h, err := New(slog.NewJSONHandler(buf, nil), cfg)
	require.NoError(t, err)
	return slog.New(h), buf
}

func lastRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var rec map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec), buf.String())
	return rec
}

func TestDefaultsDropBodiesAndCredentials(t *testing.T) {
	logger, buf := newLogger(t, &Config{})
	logger.Info("x", "body", "HbA1c 9.1", "request_body", "r", "http.response.body", "s", "Authorization", "Bearer abc", "keep", "ok")
	rec := lastRecord(t, buf)
	assert.Equal(t, "ok", rec["keep"])
	for _, k := range []string{"body", "request_body", "http.response.body", "Authorization"} {
		assert.NotContains(t, rec, k)
	}
	assert.NotContains(t, buf.String(), "HbA1c")
}

func TestTruncatesIPs(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.77":               "203.0.113.0/24",
		"203.0.113.77:51234":         "203.0.113.0/24",
		"2001:db8:1:2:3:4:5:6":       "2001:db8:1::/48",
		"[2001:db8:1:2::9]:443":      "2001:db8:1::/48",
		"::ffff:198.51.100.200":      "198.51.100.0/24",
		"fe80::1%eth0":               "fe80::/48",
		"not an address":             "[invalid-ip]",
		"198.51.100.1, 198.51.100.2": "[invalid-ip]",
	} {
		logger, buf := newLogger(t, &Config{})
		logger.Info("audit", "client_ip", in)
		assert.Equal(t, want, lastRecord(t, buf)["client_ip"], in)
	}
}

func TestTruncatesAnAddrTypedValue(t *testing.T) {
	logger, buf := newLogger(t, &Config{})
	logger.Info("audit", slog.Any("started_from", fmt.Stringer(mustAddr("192.0.2.9"))))
	assert.Equal(t, "192.0.2.0/24", lastRecord(t, buf)["started_from"])
}

func TestHashIsKeyedStableAndNotTheValue(t *testing.T) {
	cfg := &Config{Rules: EmailRules(), HashKey: []byte("k1")}
	logger, buf := newLogger(t, cfg)
	logger.Info("x", "email", "Ann@Example.com")
	first := lastRecord(t, buf)["email"].(string)
	assert.Regexp(t, `^h:[0-9a-f]{16}$`, first)
	assert.NotContains(t, buf.String(), "example.com")

	buf.Reset()
	logger.Info("x", "email", " ann@example.com ")
	assert.Equal(t, first, lastRecord(t, buf)["email"], "case and padding don't change the pseudonym")

	buf.Reset()
	logger.Info("x", "email", "bob@example.com")
	assert.NotEqual(t, first, lastRecord(t, buf)["email"])

	other, obuf := newLogger(t, &Config{Rules: EmailRules(), HashKey: []byte("k2")})
	other.Info("x", "email", "ann@example.com")
	assert.NotEqual(t, first, lastRecord(t, obuf)["email"], "a different key gives a different pseudonym")
}

func TestRandomKeyDiffersPerHandler(t *testing.T) {
	a, abuf := newLogger(t, &Config{Rules: EmailRules()})
	b, bbuf := newLogger(t, &Config{Rules: EmailRules()})
	a.Info("x", "email", "ann@example.com")
	b.Info("x", "email", "ann@example.com")
	assert.NotEqual(t, lastRecord(t, abuf)["email"], lastRecord(t, bbuf)["email"])
}

func TestErrorsLoseQuotedInputWhateverTheKey(t *testing.T) {
	const fixture = "Haemoglobin A1c 9.1 mmol"
	err := fmt.Errorf("conflict: a measurement type named %q already exists (%w)", fixture, errors.New("db"))
	logger, buf := newLogger(t, &Config{})
	logger.Error("x", "err", err, "weird_key", err, "reason", err.Error(), slog.Any("panic", "ignored"))
	assert.NotContains(t, buf.String(), fixture)
	rec := lastRecord(t, buf)
	assert.Equal(t, `conflict: a measurement type named "[redacted]" already exists (db)`, rec["err"])
	assert.Equal(t, rec["err"], rec["weird_key"])
	assert.Equal(t, rec["err"], rec["reason"])
}

func TestQuotedWithEscapes(t *testing.T) {
	assert.Equal(t, `a "[redacted]" b "[redacted]"`, redactQuoted(`a "x \" y" b "z"`))
}

func TestAppliesToGroupsAndWithAttrs(t *testing.T) {
	logger, buf := newLogger(t, &Config{Rules: EmailRules()})
	logger.With("client_ip", "203.0.113.5").WithGroup("req").Info("x", slog.Group("user", "email", "a@b.c"), "body", "secret")
	rec := lastRecord(t, buf)
	assert.Equal(t, "203.0.113.0/24", rec["client_ip"])
	user := rec["req"].(map[string]any)["user"].(map[string]any)
	assert.Regexp(t, `^h:`, user["email"])
	assert.NotContains(t, buf.String(), "secret")
	assert.NotContains(t, buf.String(), "a@b.c")
}

func TestAppRulesAdd(t *testing.T) {
	logger, buf := newLogger(t, &Config{Rules: []Rule{{Keys: []string{"client_name"}, Action: Drop}}})
	logger.Info("x", "client_name", "my agent")
	assert.NotContains(t, lastRecord(t, buf), "client_name")
}

func TestEveryDefaultKeyIsApplied(t *testing.T) {
	for _, r := range DefaultRules() {
		for _, k := range r.Keys {
			logger, buf := newLogger(t, &Config{})
			logger.Info("x", k, `"fixture"`)
			rec := lastRecord(t, buf)
			switch r.Action {
			case Drop:
				assert.NotContains(t, rec, k)
			default:
				assert.NotEqual(t, `"fixture"`, rec[k], k)
			}
			assert.NotContains(t, buf.String(), "fixture", k)
		}
	}
}

func TestEveryEmailKeyIsHashed(t *testing.T) {
	for _, k := range EmailRules()[0].Keys {
		logger, buf := newLogger(t, &Config{Rules: EmailRules()})
		logger.Info("x", k, "ann@example.com")
		assert.Regexp(t, `^h:`, lastRecord(t, buf)[k], k)
	}
}

func TestAppRuleCannotWeakenADefault(t *testing.T) {
	for _, k := range []string{"body", "BODY", "http.body", "client_ip", "err"} {
		_, err := New(slog.NewJSONHandler(&bytes.Buffer{}, nil), &Config{Rules: []Rule{{Keys: []string{k}, Action: Hash}}})
		assert.Error(t, err, k)
	}
}

func TestDottedAppRuleKeyMatchesItsLastSegment(t *testing.T) {
	logger, buf := newLogger(t, &Config{Rules: []Rule{{Keys: []string{"user.nickname"}, Action: Drop}}})
	logger.Info("x", "nickname", "bob", "profile.nickname", "bob")
	assert.NotContains(t, buf.String(), "bob")
}

func TestRejectsUnknownAction(t *testing.T) {
	_, err := New(slog.NewJSONHandler(&bytes.Buffer{}, nil), &Config{Rules: []Rule{{Keys: []string{"k"}, Action: 99}}})
	assert.Error(t, err)
}

func TestEnabledAndMessagePassThrough(t *testing.T) {
	logger, buf := newLogger(t, &Config{})
	assert.False(t, logger.Handler().Enabled(t.Context(), slog.LevelDebug))
	logger.Warn("hello", "n", 3)
	rec := lastRecord(t, buf)
	assert.Equal(t, "hello", rec["msg"])
	assert.EqualValues(t, 3, rec["n"])
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
