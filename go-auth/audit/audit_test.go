package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/audit"
	"github.com/lowaa/slop-factory/go-auth/audit/audittest"
	"github.com/lowaa/slop-factory/go-auth/clientip"
)

const (
	signIn     audit.Action = "sign_in"
	tokenReuse audit.Action = "token_reuse"
)

func record(t *testing.T, cfg *audit.Config, action audit.Action, attrs ...any) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	cfg.Logger = slog.NewJSONHandler(&buf, nil)
	r := audit.New(cfg)
	ctx := clientip.NewContext(context.Background(), netip.MustParseAddr("203.0.113.9"))
	r.Record(ctx, action, attrs...)
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line), buf.String())
	return line
}

// The line's shape is what log queries are written against: the one
// message, the action, and who it came from.
func TestAnEventIsOneLineAndOneCount(t *testing.T) {
	counter := audittest.NewCounter()
	line := record(t, &audit.Config{Counter: counter}, signIn, "user_id", "u-1", "method", "github")

	assert.Equal(t, "audit", line["msg"])
	assert.Equal(t, "sign_in", line["action"])
	assert.Equal(t, "203.0.113.9", line["client_ip"])
	assert.Equal(t, "u-1", line["user_id"])
	assert.Equal(t, "INFO", line["level"])
	assert.Equal(t, 1, counter.Count(signIn))
	assert.Equal(t, 1, counter.CountMethod(signIn, "github"), "the method attr is the count's method")
}

func TestAnEventWithoutAMethodIsCountedWithoutOne(t *testing.T) {
	counter := audittest.NewCounter()
	record(t, &audit.Config{Counter: counter}, signIn, "client_id", "c-1")
	assert.Equal(t, 1, counter.Count(signIn))
	assert.Equal(t, 0, counter.CountMethod(signIn, "c-1"))
}

// Outside a request -- no client address on the context -- an event is
// still one well-formed line, with the zero address spelled out.
func TestAnEventWithoutAClientAddress(t *testing.T) {
	var buf bytes.Buffer
	audit.New(&audit.Config{Logger: slog.NewJSONHandler(&buf, nil)}).Record(context.Background(), signIn)
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line), buf.String())
	assert.Equal(t, "sign_in", line["action"])
	assert.Equal(t, "invalid IP", line["client_ip"])
}

func TestANilCounterCountsNothing(t *testing.T) {
	line := record(t, &audit.Config{}, signIn)
	assert.Equal(t, "sign_in", line["action"])
}

func TestTheAppsWarningsAndGoAuthsOwnAreWarnings(t *testing.T) {
	cfg := func() *audit.Config { return &audit.Config{Warnings: []audit.Action{tokenReuse}} }
	for action, want := range map[audit.Action]string{
		tokenReuse:               "WARN",
		audit.CrossOriginRefused: "WARN",
		audit.CSRFRefused:        "WARN",
		signIn:                   "INFO",
	} {
		assert.Equal(t, want, record(t, cfg(), action)["level"], action)
	}
	// With no warnings of the app's own, go-auth's still are.
	assert.Equal(t, "WARN", record(t, &audit.Config{}, audit.CSRFRefused)["level"])
	assert.Equal(t, "INFO", record(t, &audit.Config{}, tokenReuse)["level"])
}

func TestCoreActionsAreTheOnesGoAuthRecords(t *testing.T) {
	assert.ElementsMatch(t, []audit.Action{audit.CrossOriginRefused, audit.CSRFRefused}, audit.CoreActions())
}
