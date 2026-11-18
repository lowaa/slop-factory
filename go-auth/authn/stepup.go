package authn

import (
	"errors"
	"time"
)

// StepUpMaxAge is how recently a session must have been authenticated
// interactively for a sensitive change -- changing how an account signs in,
// or erasing it: a session left signed in on a shared machine must not be
// enough to take the account over or destroy what it holds.
const StepUpMaxAge = 10 * time.Minute

// ErrReauthRequired refuses a sensitive change from a session whose last
// interactive authentication is older than StepUpMaxAge. Apps map it to a
// 403 with the code "reauth_required".
var ErrReauthRequired = errors.New("confirm it's you first: sign in again (with your password or a login provider) and retry")

// ReauthRequiredAfter is when who's step-up window closes: after it,
// RequireRecentAuth refuses.
func ReauthRequiredAfter(who *User) time.Time {
	return who.AuthTime.Add(StepUpMaxAge)
}

// RequireRecentAuth refuses a sensitive change from a session whose owner
// last authenticated interactively more than StepUpMaxAge ago, or at no
// recorded time at all.
func RequireRecentAuth(who *User) error {
	return RequireRecentAuthAt(who, time.Now())
}

// RequireRecentAuthAt is RequireRecentAuth against a given clock, for tests.
func RequireRecentAuthAt(who *User, now time.Time) error {
	if who.AuthTime.IsZero() || now.After(ReauthRequiredAfter(who)) {
		return ErrReauthRequired
	}
	return nil
}
