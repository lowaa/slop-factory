package authn

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// CodeCredentialRejected is the Problem code of a write refused because the
// request named a credential that was refused. Like reauth_required it is a
// 403: the caller can fix it (refresh the session) and the same request is
// then right to send again. It is not a 401, because the session client
// answers a 401 by refreshing itself, and a refusal must be seen by the page
// as a refusal, not retried blindly.
const CodeCredentialRejected = "credential_rejected"

// RefuseRejectedCredential refuses a request that names a credential that
// was refused (see CredentialRejected). Put it on the operation -- huma's
// Operation.Middlewares -- of every OptionalUser operation that changes
// state in a way that depends on who is asking.
//
// Why: an OptionalUser request with a bad credential carries on as
// anonymous, which is right for a read but wrong for a write that files
// something under "the visitor" (a consent decision in the anonymous
// cookie). The session client refreshes and retries a request answered with
// CredentialRejectedHeader, so letting the first attempt through would record
// the same act twice, under two identities. Refusing it makes the retry the
// only attempt that counts.
//
// It is a middleware on the operation, not a helper the handler calls, so
// that the refusal comes before any input is parsed and before the handler
// can act, and a route's author installs it once beside the marker instead
// of remembering a check in each handler. It must run after UserMiddleware
// (the API's middlewares always do), which is what decides the credential
// was rejected; the flag is only ever set from that verification, never from
// anything in the request. The answer is one fixed body whatever was wrong
// with the credential -- expired, revoked or forged -- so it does not say
// which tokens are real.
func RefuseRejectedCredential(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if !CredentialRejected(ctx.Context()) {
			next(ctx)
			return
		}
		// UserMiddleware already set it; repeated so the refusal carries it
		// whatever put the flag on the context.
		ctx.SetHeader(CredentialRejectedHeader, CredentialRejectedValue)
		writeProblem(api, ctx, NewProblem(http.StatusForbidden, CodeCredentialRejected,
			"your sign-in was not accepted; refresh your session and try again"))
	}
}
