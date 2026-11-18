package fleetinstructions

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrNotOwner": ErrNotOwner,
		"ErrInvalid":  ErrInvalid,
		"ErrNotFound": ErrNotFound,
		"ErrConflict": ErrConflict,
	}, map[string]string{})
}
