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

	goaudit "github.com/lowaa/slop-factory/go-auth/audit"
	"github.com/lowaa/slop-factory/go-auth/clientip"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
)

func record(t *testing.T, action audit.Action, attrs ...any) (map[string]any, *audittest.Counter) {
	t.Helper()
	var buf bytes.Buffer
	counter := audittest.NewCounter()
	r := audit.New(slog.NewJSONHandler(&buf, nil), counter)
	ctx := clientip.NewContext(context.Background(), netip.MustParseAddr("203.0.113.9"))
	r.Record(ctx, action, attrs...)
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line), buf.String())
	return line, counter
}

// The line's shape is what log queries are written against: the one
// message, the action, and who it came from.
func TestAnEventIsOneLineAndOneCount(t *testing.T) {
	line, counted := record(t, audit.SignIn, "user_id", "u-1", "method", "github")

	assert.Equal(t, "audit", line["msg"])
	assert.Equal(t, "sign_in", line["action"])
	assert.Equal(t, "203.0.113.9", line["client_ip"])
	assert.Equal(t, "u-1", line["user_id"])
	assert.Equal(t, "INFO", line["level"])
	assert.Equal(t, 1, counted.CountMethod(audit.SignIn, "github"), "the method attr is the count's method")
}

func TestAnEventWithoutAMethodIsCountedWithoutOne(t *testing.T) {
	_, counted := record(t, audit.DeviceStarted, "client_id", "c-1")
	assert.Equal(t, 1, counted.Count(audit.DeviceStarted))
	assert.Equal(t, 0, counted.CountMethod(audit.DeviceStarted, "c-1"))
}

func TestSomethingWrongIsAWarning(t *testing.T) {
	for action, want := range map[audit.Action]string{
		audit.TokenReuse:                "WARN",
		audit.ClientRegistrationRefused: "WARN",
		audit.AdminRefused:              "WARN",
		audit.CrossOriginRefused:        "WARN",
		audit.CSRFRefused:               "WARN",
		audit.ClientSecretRefused:       "WARN",
		audit.ReadOnlyRefused:           "WARN",
		audit.ConsoleToolRefused:        "WARN",
		audit.SecretWriteRefused:        "WARN",
		audit.OwnerBoundBlockerRefused:  "WARN",
		audit.BatchTreeMismatch:         "WARN",
		audit.OwnerBoundBlockerChanged:  "INFO",
		audit.ReadOnlyClientCreated:     "INFO",
		audit.SignInRefused:             "INFO",
		audit.DeviceDenied:              "INFO",
	} {
		line, _ := record(t, action)
		assert.Equal(t, want, line["level"], action)
	}
}

// Actions is what the metrics pre-create series from: an action missing
// from it would start its series at 1, hiding the first event from an alert.
func TestActionsListsTheOnesAlertsUse(t *testing.T) {
	for _, a := range []audit.Action{audit.SignInRefused, audit.ReauthRefused, audit.TokenReuse,
		audit.ClientRegistrationRefused, audit.AdminRefused, audit.DeviceDenied,
		audit.ProposalCreated, audit.ProposalApproved, audit.ProposalRejected, audit.ProposalRefused,
		audit.OwnerBoundBlockerChanged, audit.OwnerBoundBlockerRefused, audit.TicketClosedAsMerged, audit.BatchTreeMismatch,
		audit.AttachmentUploaded, audit.AttachmentDeleted} {
		assert.Contains(t, audit.Actions(), string(a))
	}
}

// go-auth's own refusals are slop-haus events too, and need their series
// as much as slop-haus's.
func TestActionsListGoAuthsOwn(t *testing.T) {
	for _, a := range goaudit.CoreActions() {
		assert.Contains(t, audit.Actions(), string(a))
	}
}

func TestActionsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range audit.Actions() {
		assert.False(t, seen[a], "%s listed twice", a)
		seen[a] = true
	}
}
