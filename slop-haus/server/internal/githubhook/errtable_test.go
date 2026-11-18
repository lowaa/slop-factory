package githubhook

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrDisabled":     ErrDisabled,
		"ErrUnauthorized": ErrUnauthorized,
		"ErrBadDelivery":  ErrBadDelivery,
	}, map[string]string{
		"ErrUnauthorized": "answered by the receiver (receiver.go), which gives nothing away",
		"ErrBadDelivery":  "answered by the receiver (receiver.go)",
	})
}
