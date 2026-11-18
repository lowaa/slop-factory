package manifest

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrInvalid":         ErrInvalid,
		"ErrNotOwner":        ErrNotOwner,
		"ErrStaleRevision":   ErrStaleRevision,
		"ErrMoveBlocked":     ErrMoveBlocked,
		"ErrSyncBlocked":     ErrSyncBlocked,
		"ErrSyncUnavailable": ErrSyncUnavailable,
	}, map[string]string{
		"ErrStaleRevision": "a refusal of the store that the service does not pass on to the handler",
	})
}
