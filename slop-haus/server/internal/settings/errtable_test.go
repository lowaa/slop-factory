package settings

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrInvalidSetting": ErrInvalidSetting,
		"ErrUnknownSetting": ErrUnknownSetting,
		"ErrTypeMismatch":   ErrTypeMismatch,
		"ErrNotStored":      ErrNotStored,
		"ErrNotOwner":       ErrNotOwner,
		"ErrNoActor":        ErrNoActor,
	}, map[string]string{
		"ErrNotStored": "the service reads it as the default; it never reaches a handler",
	})
}
