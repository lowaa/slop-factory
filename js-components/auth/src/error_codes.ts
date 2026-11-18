// The machine-readable codes the auth servers in this repo refuse with --
// every app answers the same refusal with the same code, which is what
// lets one set of pages serve them all. Explained here in
// words a person can act on, rather than each app mapping them itself.
//
// provider is the display name of the provider involved when the page
// knows it; a callback refusal arrives as a bare code, so on an app with
// more than one provider it may not.
export function describeAuthCode(code: string, provider?: string): string | undefined {
  const who = provider ?? "your sign-in provider";
  const account = provider ? `That ${provider} account` : "That account";
  switch (code) {
    case "reauth_required":
      return "Confirm it’s you before changing how you sign in.";
    case "deletion_scheduled":
      return "This account is scheduled for deletion, so it can’t be used. You can cancel the deletion to keep it.";
    case "undo_window_closed":
      return "It’s too late to cancel: the waiting period has passed and the account is about to be erased.";
    case "sole_owner":
      return "You’re the only owner of a workspace that other people use, so it can’t be deleted with you in it. Make another member an owner first.";
    case "confirmation_mismatch":
      return "Type the phrase exactly as shown to confirm.";
    case "last_method":
      return "That’s the only way you can sign in, so it can’t be removed. Add another way first.";
    case "identity_in_use":
      return `${account} is already linked to a different account here.`;
    case "already_linked":
      return `${account} is already linked to this account.`;
    case "provider_already_linked":
      return `This account already has ${provider ? `a ${provider}` : "that kind of"} sign-in linked. Remove it first to link another.`;
    case "identity_mismatch":
      return `${account} isn’t linked to the account you’re signed in as.`;
    case "session_mismatch":
      return "You’re no longer signed in as the account that started this. Sign in and try again.";
    case "no_account":
      return `There’s no account here for that ${who} sign-in. If you already have an account, sign in another way and link ${who} from your account page. Otherwise, ask for an invite.`;
    case "account_exists":
      return "There’s already an account at this invite’s address. Sign in to it, then open the invite again.";
    case "password_exists":
      return "This account already has a password. Sign in with it, or reset it.";
    case "login_expired":
      return "That sign-in took too long, or was started somewhere else. Please try again.";
    case "login_cancelled":
      return "The sign-in was cancelled.";
    case "provider_unavailable":
      return `${provider ?? "The sign-in provider"} isn’t available right now. Try again shortly.`;
    case "provider_error":
      return `${provider ?? "The sign-in provider"} couldn’t confirm who you are. Please try again.`;
    case "unknown_provider":
      return "That way of signing in isn’t available here.";
    case "invalid_token":
      return "This link has expired or has already been used.";
    case "email_unavailable":
      return "Email isn’t set up here, so this can’t be done right now.";
    case "age_requirement":
      return "You need to be old enough to use this service. If you are under the minimum age, ask a parent or guardian.";
    case "signup_consent_required":
      return "To make an account, start from the sign-up page, where you are asked to agree to what it needs first.";
    case "document_changed":
      return "The notice you were shown has changed. Reload the page, read it again and agree to continue.";
    case "rate_limited":
      return "Too many attempts. Wait a little and try again.";
    case "post_required":
      return "Start again from this site’s own page.";
    case "internal":
      return "Something went wrong. Please try again.";
    default:
      return undefined;
  }
}

// readAndClearCallbackError reads the code a refused provider callback
// sent the browser back with (`?error=<code>`), and scrubs it from the
// address bar so a reload or a shared link doesn't show it again. Only the
// query parameter goes; the rest of the URL is left as it was.
export function readAndClearCallbackError(): string | null {
  const url = new URL(window.location.href);
  const code = url.searchParams.get("error");
  if (code === null) return null;
  url.searchParams.delete("error");
  window.history.replaceState(window.history.state, "", url.pathname + url.search + url.hash);
  return code;
}

export interface ErrorDescribers {
  errorCode?: (err: unknown) => string | undefined;
  describeError: (err: unknown) => string;
}

// explainError is the message for a failed call: this package's own words
// for a code it knows, else whatever the app says about the error.
export function explainError(err: unknown, describers: ErrorDescribers, provider?: string): string {
  const code = describers.errorCode?.(err);
  return (code && describeAuthCode(code, provider)) || describers.describeError(err);
}

// explainCallbackError is the message for the code a refused provider
// round trip came back with. The code alone doesn't say which provider it
// was, so one is named only when there is just one it could have been.
export function explainCallbackError(code: string, providers: { displayName: string }[]): string {
  const provider = providers.length === 1 ? providers[0].displayName : undefined;
  return describeAuthCode(code, provider) ?? describeAuthCode("internal")!;
}
