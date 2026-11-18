package oauth

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrAgentUnavailable":       ErrAgentUnavailable,
		"ErrAgentNameTaken":         ErrAgentNameTaken,
		"ErrAgentKindInvalid":       ErrAgentKindInvalid,
		"ErrSlotCapInvalid":         ErrSlotCapInvalid,
		"ErrReplaceUnavailable":     ErrReplaceUnavailable,
		"ErrNotFound":               ErrNotFound,
		"ErrNoApprover":             ErrNoApprover,
		"ErrSlotCredentialsOff":     ErrSlotCredentialsOff,
		"ErrTooManySlotCredentials": ErrTooManySlotCredentials,
		"ErrTooManyClients":         ErrTooManyClients,
		"ErrInvalidToken":           ErrInvalidToken,
		"ErrUntrustedRedirect":      ErrUntrustedRedirect,
		"ErrTooManyLookups":         ErrTooManyLookups,
	}, map[string]string{
		"ErrInvalidToken":           "answered by the bearer-token check, not by the device-approval handler",
		"ErrUntrustedRedirect":      "answered with its own HTML page by the authorize endpoint (http.go)",
		"ErrNoApprover":             "internal: surfaced to the supervisor as a failed token grant, not by this handler",
		"ErrSlotCredentialsOff":     "returned to the token endpoint, which speaks OAuth errors, not this handler",
		"ErrTooManySlotCredentials": "returned to the token endpoint, which speaks OAuth errors, not this handler",
		"ErrTooManyClients":         "turned into errRegistrationFull by the service before it reaches a handler",
	})
}
