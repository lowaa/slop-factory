import { useState, type FormEvent, type ReactNode } from "react";
import { Button } from "@slop-factory/ui";
import { useAuthPages, type ExtraValues, type LoginProvider } from "../config";
import { AuthNotice } from "../auth_layout/auth_layout";
import ProviderButtons, { OrDivider } from "../provider_buttons/provider_buttons";
import { explainCallbackError, explainError } from "../error_codes";

export interface InviteAcceptProps {
  // Accepts with a password: whatever the app's endpoint needs besides it
  // (a display name, say) comes from passwordFields below. Resolving means
  // accepted; the app moves on (signs in, reloads).
  // extras is what the app's signUpExtras collected; given only when it
  // configured some.
  onAcceptWithPassword: (password: string, extras?: ExtraValues) => Promise<void>;
  // Starts accepting through a provider, resolving with where to send the
  // browser -- a POST, since it acts on an account (see either server's
  // README). Absent, or with no providers configured, no buttons show.
  onContinueWithProvider?: (provider: string, extras?: ExtraValues) => Promise<string>;
  // Extra fields for the password option specifically (a display name the
  // server can't otherwise get, say) -- rendered with the password field,
  // never with the provider buttons: a provider round trip supplies its own
  // answer for whatever these ask (the account's name, say), so typing one
  // in here and then choosing a provider would silently throw it away.
  // providerFieldsNote says as much next to the provider buttons, whenever
  // there are both providers on offer and fields here to explain away.
  passwordFields?: ReactNode;
  // Fields for either way in, shown above both the password form and the
  // provider buttons -- what the invite makes (a tenant's name, say), which
  // does not depend on how the person proves who they are. Unlike
  // signUpExtras they belong to this page alone.
  sharedFields?: ReactNode;
  providerFieldsNote?: ReactNode;
  // Shown above everything: who invited them to what.
  intro?: ReactNode;
  // A refused provider round trip's code, shown as the error.
  callbackError?: string | null;
  submitLabel?: string;
  submittingLabel?: string;
  passwordLabel?: string;
}

// The part of an invite page every app shares: set a password, or continue
// with a provider. An app composes its own page around it -- the invite
// preview, names, tenant wording -- since those differ between apps.
export default function InviteAccept({
  onAcceptWithPassword,
  onContinueWithProvider,
  passwordFields,
  sharedFields,
  providerFieldsNote = "Continuing with a provider uses its own name for the account, not what’s typed below.",
  intro,
  callbackError,
  submitLabel = "Accept invite",
  submittingLabel = "Accepting…",
  passwordLabel = "Choose a password",
}: InviteAcceptProps) {
  const { providers, passwordLogin, passwordRules, copy, describeError, errorCode, redirect, signUpExtras } =
    useAuthPages();
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(() =>
    callbackError ? explainCallbackError(callbackError, providers) : null,
  );
  const [extras, setExtras] = useState<ExtraValues>({});
  const extrasComplete = signUpExtras ? signUpExtras.isComplete(extras) : true;
  const offered = onContinueWithProvider ? providers : [];

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    // Not only the disabled button: a form can be submitted without it.
    if (!extrasComplete) return;
    setError(null);
    setSubmitting(true);
    try {
      // The second argument only exists for an app that configured extras, so
      // an app that did not sees the call it always did.
      await (signUpExtras ? onAcceptWithPassword(password, extras) : onAcceptWithPassword(password));
    } catch (err) {
      setError(explainError(err, { errorCode, describeError }));
      setSubmitting(false);
    }
  }

  async function continueWith(p: LoginProvider) {
    setError(null);
    try {
      redirect(await (signUpExtras ? onContinueWithProvider!(p.name, extras) : onContinueWithProvider!(p.name)));
    } catch (err) {
      setError(explainError(err, { errorCode, describeError }, p.displayName));
      throw err;
    }
  }

  const providerButtons = (
    <ProviderButtons
      providers={offered}
      label={copy.continueWithProvider}
      onChoose={continueWith}
      disabled={submitting || !extrasComplete}
    />
  );

  // Above the provider buttons as well as the password: either way in makes
  // the account, so either needs what the app asks to be agreed.
  const extraFields = (
    <>
      {sharedFields}
      {signUpExtras && <signUpExtras.Fields values={extras} onChange={setExtras} disabled={submitting} />}
    </>
  );

  if (!passwordLogin) {
    return (
      <div className="auth-layout__body">
        {intro}
        {extraFields}
        {error && <AuthNotice variant="error">{error}</AuthNotice>}
        {providerButtons}
      </div>
    );
  }

  return (
    <form className="auth-layout__form" onSubmit={handleSubmit}>
      {intro}
      {extraFields}
      {offered.length > 0 && (
        <>
          {providerButtons}
          {passwordFields && <p className="auth-layout__switch">{providerFieldsNote}</p>}
          <OrDivider label={copy.orDivider} />
        </>
      )}

      {passwordFields}

      <label className="auth-layout__field">
        <span>{passwordLabel}</span>
        <input
          type="password"
          autoComplete="new-password"
          required
          minLength={passwordRules?.minLength}
          maxLength={passwordRules?.maxLength}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      </label>

      {error && <AuthNotice variant="error">{error}</AuthNotice>}

      <Button
        type="submit"
        variant="primary"
        className="auth-layout__submit"
        disabled={submitting || !extrasComplete}
      >
        {submitting ? submittingLabel : submitLabel}
      </Button>
    </form>
  );
}
