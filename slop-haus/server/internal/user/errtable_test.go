package user

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrRateLimited":           ErrRateLimited,
		"ErrTenantColourUnknown":   ErrTenantColourUnknown,
		"ErrTenantNameEmpty":       ErrTenantNameEmpty,
		"ErrTenantNameTooLong":     ErrTenantNameTooLong,
		"ErrTenantNameInvalid":     ErrTenantNameInvalid,
		"ErrTenantNameTaken":       ErrTenantNameTaken,
		"ErrInvalidCredentials":    ErrInvalidCredentials,
		"ErrInvalidToken":          ErrInvalidToken,
		"ErrInvalidSession":        ErrInvalidSession,
		"ErrPasswordTooLong":       ErrPasswordTooLong,
		"ErrNotFound":              ErrNotFound,
		"ErrNotOwner":              ErrNotOwner,
		"ErrTenantLimit":           ErrTenantLimit,
		"ErrLastActiveTenant":      ErrLastActiveTenant,
		"ErrUnarchiveOverLimit":    ErrUnarchiveOverLimit,
		"ErrAgentsUnavailable":     ErrAgentsUnavailable,
		"ErrArchiveNameMismatch":   ErrArchiveNameMismatch,
		"ErrTenantCreationOff":     ErrTenantCreationOff,
		"ErrPasswordSignInOff":     ErrPasswordSignInOff,
		"ErrRegistrationClosed":    ErrRegistrationClosed,
		"ErrAccountExists":         ErrAccountExists,
		"ErrWrongAccount":          ErrWrongAccount,
		"ErrAlreadyMember":         ErrAlreadyMember,
		"ErrNotMember":             ErrNotMember,
		"ErrTenantArchived":        ErrTenantArchived,
		"ErrPasswordTooShort":      ErrPasswordTooShort,
		"ErrReauthRequired":        ErrReauthRequired,
		"ErrNotSignedIn":           ErrNotSignedIn,
		"ErrIdentityTaken":         ErrIdentityTaken,
		"ErrAlreadyLinked":         ErrAlreadyLinked,
		"ErrProviderAlreadyLinked": ErrProviderAlreadyLinked,
		"ErrLastMethod":            ErrLastMethod,
		"ErrNoAccount":             ErrNoAccount,
		"ErrSessionMismatch":       ErrSessionMismatch,
		"ErrIdentityMismatch":      ErrIdentityMismatch,
		"ErrUnknownProvider":       ErrUnknownProvider,
		"ErrProviderUnavailable":   ErrProviderUnavailable,
		"ErrProviderFailed":        ErrProviderFailed,
		"ErrUnknownIntent":         ErrUnknownIntent,
		"ErrLoginExpired":          ErrLoginExpired,
		"ErrLoginCancelled":        ErrLoginCancelled,
		"ErrPostRequired":          ErrPostRequired,
	}, map[string]string{})
}
