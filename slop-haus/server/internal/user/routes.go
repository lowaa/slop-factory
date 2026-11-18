package user

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// TenantMovingOperations are the signed-in operations that run from a page
// whose tenant is no longer the session's, because moving between tenants
// (and listing them, to say which the session is in now) is what they are
// for: authn.TenantGuard lets them through.
var TenantMovingOperations = []string{"switchTenant", "acceptInvite", "listTenants", "createTenant", "archiveTenant", "unarchiveTenant", "listArchivedTenants"}

// RegisterRoutes mounts sign-up, sign-in, invitations and the account
// routes. The exchanges that come before a session exists are Public;
// inviting people to start tenants is an admin's; the rest need the
// signed-in user.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:      authn.Public(),
		OperationID:   "register",
		Method:        http.MethodPost,
		Path:          "/api/auth/register",
		Summary:       "Sign up; the activation link is emailed",
		DefaultStatus: http.StatusAccepted,
	}, h.Register)
	huma.Register(api, huma.Operation{
		Metadata:      authn.Public(),
		OperationID:   "activate",
		Method:        http.MethodPost,
		Path:          "/api/auth/activate",
		Summary:       "Activate an account with its emailed token",
		DefaultStatus: http.StatusNoContent,
	}, h.Activate)
	huma.Register(api, huma.Operation{
		Metadata:    authn.Public(),
		OperationID: "login",
		Method:      http.MethodPost,
		Path:        "/api/auth/login",
		Summary:     "Sign in; the session is set as cookies",
	}, h.Login)
	huma.Register(api, huma.Operation{
		Metadata:      authn.Public(),
		OperationID:   "refreshSession",
		Method:        http.MethodPost,
		Path:          RefreshPath,
		Summary:       "Exchange the refresh cookie for a new access cookie",
		DefaultStatus: http.StatusNoContent,
	}, h.Refresh)
	huma.Register(api, huma.Operation{
		// Public so a session whose access token has already expired can
		// still be signed out.
		Metadata:      authn.Public(),
		OperationID:   "logout",
		Method:        http.MethodPost,
		Path:          "/api/auth/logout",
		Summary:       "Sign out of this browser",
		DefaultStatus: http.StatusNoContent,
	}, h.Logout)
	huma.Register(api, huma.Operation{
		Metadata:      authn.Public(),
		OperationID:   "requestPasswordReset",
		Method:        http.MethodPost,
		Path:          "/api/auth/password-reset",
		Summary:       "Email a password reset link, if the address has an account",
		DefaultStatus: http.StatusAccepted,
	}, h.RequestPasswordReset)
	huma.Register(api, huma.Operation{
		Metadata:      authn.Public(),
		OperationID:   "resetPassword",
		Method:        http.MethodPost,
		Path:          "/api/auth/password-reset/confirm",
		Summary:       "Set a new password with an emailed reset token",
		DefaultStatus: http.StatusNoContent,
	}, h.ResetPassword)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getMe",
		Method:      http.MethodGet,
		Path:        "/api/auth/me",
		Summary:     "The signed-in user's profile",
	}, h.Me)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "renameTenant",
		Method:      http.MethodPatch,
		Path:        "/api/tenant",
		Summary:     "Rename the current tenant; its owner only",
		Errors:      []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
	}, h.RenameTenant)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "setTenantColour",
		Method:      http.MethodPut,
		Path:        "/api/tenant/colour",
		Summary:     "Set or clear the current tenant's colour; its owner only",
		Errors:      []int{http.StatusForbidden, http.StatusUnprocessableEntity},
	}, h.SetTenantColour)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listMembers",
		Method:      http.MethodGet,
		Path:        "/api/members",
		Summary:     "Everyone in the signed-in user's tenant",
	}, h.Members)

	huma.Register(api, huma.Operation{
		Metadata:    authn.Public(),
		OperationID: "getAuthConfig",
		Method:      http.MethodGet,
		Path:        "/api/auth/config",
		Summary:     "What the sign-in page should offer",
	}, h.AuthConfig)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireAdmin(),
		OperationID:   "adminCheck",
		Method:        http.MethodGet,
		Path:          "/api/auth/admin-check",
		Summary:       "204 for an admin; asked by nginx before serving the admin pages",
		DefaultStatus: http.StatusNoContent,
	}, h.AdminCheck)

	// An admin's invitations to start a tenant.
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireAdmin(),
		OperationID:   "createAdminInvite",
		Method:        http.MethodPost,
		Path:          "/api/admin/invites",
		Summary:       "Invite someone to start a tenant of their own",
		DefaultStatus: http.StatusCreated,
	}, h.CreateAdminInvite)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireAdmin(),
		OperationID: "listAdminInvites",
		Method:      http.MethodGet,
		Path:        "/api/admin/invites",
		Summary:     "Outstanding invitations to start a tenant",
	}, h.ListAdminInvites)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireAdmin(),
		OperationID:   "revokeAdminInvite",
		Method:        http.MethodDelete,
		Path:          "/api/admin/invites/{id}",
		Summary:       "Withdraw an invitation to start a tenant",
		DefaultStatus: http.StatusNoContent,
	}, h.RevokeAdminInvite)

	// A member's invitations into their tenant.
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "createInvite",
		Method:        http.MethodPost,
		Path:          "/api/invites",
		Summary:       "Invite someone into the current tenant",
		DefaultStatus: http.StatusCreated,
	}, h.CreateInvite)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listInvites",
		Method:      http.MethodGet,
		Path:        "/api/invites",
		Summary:     "Outstanding invitations into the current tenant",
	}, h.ListInvites)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "revokeInvite",
		Method:        http.MethodDelete,
		Path:          "/api/invites/{id}",
		Summary:       "Withdraw an invitation into the current tenant",
		DefaultStatus: http.StatusNoContent,
	}, h.RevokeInvite)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "acceptInvite",
		Method:      http.MethodPost,
		Path:        "/api/invites/accept",
		Summary:     "Accept an invitation as the signed-in user, moving the session into its tenant",
	}, h.AcceptInvite)

	// The invite link's page, before there is an account.
	huma.Register(api, huma.Operation{
		Metadata:    authn.Public(),
		OperationID: "previewInvite",
		Method:      http.MethodPost,
		Path:        "/api/auth/invite/preview",
		Summary:     "Describe an invitation for its link's page",
	}, h.PreviewInvite)
	huma.Register(api, huma.Operation{
		Metadata:    authn.Public(),
		OperationID: "acceptInviteAsNewAccount",
		Method:      http.MethodPost,
		Path:        "/api/auth/invite/accept",
		Summary:     "Accept an invitation by creating an account at its address; signs in",
	}, h.AcceptInviteAsNewAccount)

	// Signing in through a login provider. A plain sign-in starts, and every
	// round trip ends, with a navigation: Public GETs, where the state
	// cookie, not a CSRF check, binds a callback to the browser that
	// started it. The round trips that act on an account start from POSTs,
	// which authn.BrowserGuard checks like every write.
	huma.Register(api, huma.Operation{
		Metadata:    authn.Public(),
		OperationID: "startProviderSignIn",
		Method:      http.MethodGet,
		Path:        "/api/auth/login/{provider}",
		Summary:     "Start signing in with a login provider; redirects there",
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound, http.StatusServiceUnavailable},
	}, h.StartSignIn)
	huma.Register(api, huma.Operation{
		Metadata:    authn.Public(),
		OperationID: "startProviderInvite",
		Method:      http.MethodPost,
		Path:        "/api/auth/invite/accept/{provider}",
		Summary:     "Start accepting an invitation with a login provider identity; answers where to send the browser",
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound, http.StatusServiceUnavailable},
	}, h.StartInvite)
	huma.Register(api, huma.Operation{
		Metadata:    authn.Public(),
		OperationID: "providerSignInCallback",
		Method:      http.MethodGet,
		Path:        CallbackPath("{provider}"),
		Summary:     "Where a login provider sends the browser back; sets the session and redirects on, or back to the board's page for the round trip with ?error=<code>",
	}, h.SignInCallback)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "startProviderLink",
		Method:      http.MethodPost,
		Path:        "/api/auth/link/{provider}",
		Summary:     "Start linking a login provider identity to the signed-in account (needs a recent sign-in)",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable},
	}, h.StartLink)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "startProviderReauth",
		Method:      http.MethodPost,
		Path:        "/api/auth/reauth/{provider}",
		Summary:     "Start confirming it's you with a login provider, for a change that needs a recent sign-in",
		Errors:      []int{http.StatusNotFound, http.StatusServiceUnavailable},
	}, h.StartReauth)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "reauthWithPassword",
		Method:        http.MethodPost,
		Path:          "/api/auth/reauth/password",
		Summary:       "Confirm it's you with your password, for a change that needs a recent sign-in",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusTooManyRequests},
	}, h.ReauthPassword)

	// The signed-in account's ways of signing in. Every change needs a
	// recent interactive sign-in, else 403 with code reauth_required.
	const methods = "/api/auth/sign-in-methods"
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listSignInMethods",
		Method:      http.MethodGet,
		Path:        methods,
		Summary:     "The signed-in account's ways of signing in",
	}, h.SignInMethods)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "setPassword",
		Method:        http.MethodPut,
		Path:          methods + "/password",
		Summary:       "Add a password, or change it, ending every other session (needs a recent sign-in)",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusForbidden, http.StatusUnprocessableEntity},
	}, h.SetPassword)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "removePassword",
		Method:        http.MethodDelete,
		Path:          methods + "/password",
		Summary:       "Remove the password, unless it is the last way in (needs a recent sign-in)",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, h.RemovePassword)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "removeIdentity",
		Method:        http.MethodDelete,
		Path:          methods + "/identity",
		Summary:       "Unlink a login provider identity, unless it is the last way in (needs a recent sign-in)",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, h.RemoveIdentity)

	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listTenants",
		Method:      http.MethodGet,
		Path:        "/api/auth/tenants",
		Summary:     "The tenants the signed-in user belongs to",
	}, h.Tenants)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "createTenant",
		Method:      http.MethodPost,
		Path:        "/api/tenants",
		Summary:     "Create a tenant owned by the signed-in user (up to the owner limit), moving the session into it",
		Errors:      []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}, h.CreateTenant)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "switchTenant",
		Method:      http.MethodPost,
		Path:        "/api/auth/switch-tenant",
		Summary:     "Move the session into another of the user's tenants",
	}, h.SwitchTenant)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listArchivedTenants",
		Method:      http.MethodGet,
		Path:        "/api/tenants/archived",
		Summary:     "The tenants the signed-in user has archived (owners only see any)",
	}, h.ArchivedTenants)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "archiveTenant",
		Method:      http.MethodPost,
		Path:        "/api/tenants/{id}/archive",
		Summary:     "Archive a tenant the caller owns, switching off its agents; needs a recent sign-in and the tenant's exact name. A session in it moves to another of the caller's active tenants. Archiving an archived tenant retries the agents' switch-off",
		Errors:      []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, h.ArchiveTenant)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "unarchiveTenant",
		Method:        http.MethodPost,
		Path:          "/api/tenants/{id}/unarchive",
		Summary:       "Bring back an archived tenant the caller owns, unless they own the most active tenants allowed; needs a recent sign-in. Its agents stay off",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusForbidden},
	}, h.UnarchiveTenant)
}
