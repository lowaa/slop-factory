package evalset

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrInvalid":         ErrInvalid,
		"ErrNotFound":        ErrNotFound,
		"ErrNameTaken":       ErrNameTaken,
		"ErrChanged":         ErrChanged,
		"ErrEmpty":           ErrEmpty,
		"ErrReplaysDisabled": ErrReplaysDisabled,
	}, map[string]string{})
}
