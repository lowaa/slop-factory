package user

import (
	"net/http"

	goauthn "github.com/lowaa/slop-factory/go-auth/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// problems is how each of this package's refusals is answered over HTTP.
var problems = goauthn.NewProblemTable([]string{"user: "},
	goauthn.ProblemEntry{Err: ErrInvalidCredentials, Status: http.StatusUnauthorized, Code: "invalid_credentials"},
	goauthn.ProblemEntry{Err: ErrInvalidSession, Status: http.StatusUnauthorized, Code: "invalid_session"},
	goauthn.ProblemEntry{Err: ErrNotSignedIn, Status: http.StatusUnauthorized, Code: "not_signed_in"},
	goauthn.ProblemEntry{Err: ErrInvalidToken, Status: http.StatusBadRequest, Code: "invalid_token"},
	goauthn.ProblemEntry{Err: ErrPasswordTooLong, Status: http.StatusUnprocessableEntity, Code: "password_too_long"},
	goauthn.ProblemEntry{Err: ErrPasswordTooShort, Status: http.StatusUnprocessableEntity, Code: "password_too_short"},
	goauthn.ProblemEntry{Err: ErrRateLimited, Status: http.StatusTooManyRequests, Code: "rate_limited"},
	goauthn.ProblemEntry{Err: ErrRegistrationClosed, Status: http.StatusForbidden, Code: "registration_closed"},
	goauthn.ProblemEntry{Err: ErrPasswordSignInOff, Status: http.StatusForbidden, Code: "password_sign_in_off"},
	goauthn.ProblemEntry{Err: ErrNotMember, Status: http.StatusForbidden, Code: "not_member"},
	goauthn.ProblemEntry{Err: ErrTenantArchived, Status: http.StatusForbidden, Code: authn.CodeTenantArchived},
	goauthn.ProblemEntry{Err: ErrNotOwner, Status: http.StatusForbidden, Code: "not_owner"},
	goauthn.ProblemEntry{Err: ErrTenantNameEmpty, Status: http.StatusUnprocessableEntity, Code: "tenant_name_empty"},
	goauthn.ProblemEntry{Err: ErrTenantNameTooLong, Status: http.StatusUnprocessableEntity, Code: "tenant_name_too_long"},
	goauthn.ProblemEntry{Err: ErrTenantNameInvalid, Status: http.StatusUnprocessableEntity, Code: "tenant_name_invalid"},
	goauthn.ProblemEntry{Err: ErrTenantColourUnknown, Status: http.StatusUnprocessableEntity, Code: "tenant_colour_unknown"},
	goauthn.ProblemEntry{Err: ErrTenantLimit, Status: http.StatusForbidden, Code: "tenant_limit_reached"},
	goauthn.ProblemEntry{Err: ErrLastActiveTenant, Status: http.StatusConflict, Code: "last_active_tenant"},
	goauthn.ProblemEntry{Err: ErrUnarchiveOverLimit, Status: http.StatusForbidden, Code: "tenant_limit_reached"},
	goauthn.ProblemEntry{Err: ErrArchiveNameMismatch, Status: http.StatusUnprocessableEntity, Code: "confirm_name_mismatch"},
	goauthn.ProblemEntry{Err: ErrAgentsUnavailable, Status: http.StatusServiceUnavailable, Code: "archive_unavailable"},
	goauthn.ProblemEntry{Err: ErrTenantCreationOff, Status: http.StatusForbidden, Code: "tenant_creation_off"},
	goauthn.ProblemEntry{Err: ErrTenantNameTaken, Status: http.StatusConflict, Code: "tenant_name_taken"},
	goauthn.ProblemEntry{Err: goauthn.ErrReauthRequired, Status: http.StatusForbidden, Code: "reauth_required"},
	goauthn.ProblemEntry{Err: ErrNoAccount, Status: http.StatusForbidden, Code: "no_account"},
	goauthn.ProblemEntry{Err: ErrSessionMismatch, Status: http.StatusForbidden, Code: "session_mismatch"},
	goauthn.ProblemEntry{Err: ErrIdentityMismatch, Status: http.StatusForbidden, Code: "identity_mismatch"},
	goauthn.ProblemEntry{Err: ErrAccountExists, Status: http.StatusConflict, Code: "account_exists"},
	goauthn.ProblemEntry{Err: ErrWrongAccount, Status: http.StatusConflict, Code: "wrong_account"},
	goauthn.ProblemEntry{Err: ErrAlreadyMember, Status: http.StatusConflict, Code: "already_member"},
	goauthn.ProblemEntry{Err: ErrIdentityTaken, Status: http.StatusConflict, Code: "identity_in_use"},
	goauthn.ProblemEntry{Err: ErrAlreadyLinked, Status: http.StatusConflict, Code: "already_linked"},
	goauthn.ProblemEntry{Err: ErrProviderAlreadyLinked, Status: http.StatusConflict, Code: "provider_already_linked"},
	goauthn.ProblemEntry{Err: ErrLastMethod, Status: http.StatusConflict, Code: "last_method"},
	goauthn.ProblemEntry{Err: ErrNotFound, Status: http.StatusNotFound, Code: "not_found"},
	goauthn.ProblemEntry{Err: ErrUnknownProvider, Status: http.StatusNotFound, Code: "unknown_provider"},
	goauthn.ProblemEntry{Err: ErrUnknownIntent, Status: http.StatusBadRequest, Code: "unknown_intent"},
	goauthn.ProblemEntry{Err: ErrPostRequired, Status: http.StatusBadRequest, Code: "post_required"},
	goauthn.ProblemEntry{Err: ErrLoginExpired, Status: http.StatusBadRequest, Code: "login_expired"},
	goauthn.ProblemEntry{Err: ErrLoginCancelled, Status: http.StatusBadRequest, Code: "login_cancelled"},
	goauthn.ProblemEntry{Err: ErrProviderFailed, Status: http.StatusBadGateway, Code: "provider_error"},
	goauthn.ProblemEntry{Err: ErrProviderUnavailable, Status: http.StatusServiceUnavailable, Code: "provider_unavailable"},
)

// ProblemFor is err as an HTTP answer, or nil if it is not one of this
// package's refusals.
func ProblemFor(err error) *authn.Problem {
	return problems.ProblemFor(err)
}
