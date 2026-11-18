package attachment

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func RequireErrTableCovers(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrNotFound":         ErrNotFound,
		"ErrUnsupportedType":  ErrUnsupportedType,
		"ErrEmpty":            ErrEmpty,
		"ErrTooLarge":         ErrTooLarge,
		"ErrQuotaExceeded":    ErrQuotaExceeded,
		"ErrUnavailable":      ErrUnavailable,
		"ErrNoFile":           ErrNoFile,
		"ErrNotMultipart":     ErrNotMultipart,
		"ErrNotPermitted":     ErrNotPermitted,
		"ErrSecretInFilename": ErrSecretInFilename,
	}, map[string]string{})
}

var ReadCredentials = readCredentials

var NewLocalStore = newLocalStore
