package reports

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	t.Parallel()
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrInvalid": ErrInvalid,
		"ErrTooSlow": ErrTooSlow,
	}, map[string]string{})
}
