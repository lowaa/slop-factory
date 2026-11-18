package usersettings

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrUnknownSetting": ErrUnknownSetting,
		"ErrTypeMismatch":   ErrTypeMismatch,
		"ErrNotMember":      ErrNotMember,
	}, map[string]string{})
}
