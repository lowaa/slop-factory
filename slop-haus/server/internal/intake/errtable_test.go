package intake

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrDisabled":      ErrDisabled,
		"ErrHMACDisabled":  ErrHMACDisabled,
		"ErrNotFound":      ErrNotFound,
		"ErrUnauthorized":  ErrUnauthorized,
		"ErrRateLimited":   ErrRateLimited,
		"ErrInvalidSource": ErrInvalidSource,
		"ErrBadDelivery":   ErrBadDelivery,
		"ErrNotOwner":      ErrNotOwner,
	}, map[string]string{
		"ErrUnauthorized": "answered by the receiver (receiver.go), which gives nothing away",
		"ErrRateLimited":  "answered by the receiver (receiver.go)",
		"ErrBadDelivery":  "answered by the receiver (receiver.go)",
	})
}
