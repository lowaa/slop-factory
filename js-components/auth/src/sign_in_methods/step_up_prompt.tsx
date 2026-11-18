import { useState, type FormEvent } from "react";
import { Button } from "@slop-factory/ui";
import type { LoginProvider } from "../config";
import { AuthNotice } from "../auth_layout/auth_layout";
import ProviderButtons from "../provider_buttons/provider_buttons";
import type { SignInMethodsCopy } from "./copy";

interface StepUpPromptProps {
  copy: SignInMethodsCopy;
  // Whether the account has a password it can confirm with.
  withPassword: boolean;
  // Providers the account has a usable identity at: a step-up only accepts
  // the signed-in account's own identity, so no other is worth offering.
  providers: LoginProvider[];
  // Confirms with the password and retries the change; a rejection is shown
  // here, so the person can try again.
  onPassword: (password: string) => Promise<void>;
  onProvider: (provider: LoginProvider) => Promise<void>;
  onCancel: () => void;
  // The message for a failed password confirmation.
  describePasswordError: (err: unknown) => string;
}

// Shown when a change came back reauth_required: the session's sign-in is
// too old for a change to how the account signs in, so the person proves
// it's them again -- with their password, or through a provider they've
// linked -- and the change is retried.
export default function StepUpPrompt({
  copy,
  withPassword,
  providers,
  onPassword,
  onProvider,
  onCancel,
  describePasswordError,
}: StepUpPromptProps) {
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await onPassword(password);
    } catch (err) {
      setError(describePasswordError(err));
      setSubmitting(false);
    }
  }

  return (
    <section className="sign-in-methods__step-up" aria-labelledby="sign-in-methods-step-up-title">
      <h3 id="sign-in-methods-step-up-title" className="sign-in-methods__step-up-title">
        {copy.stepUpTitle}
      </h3>
      <p className="sign-in-methods__muted">{copy.stepUpIntro}</p>

      {withPassword && (
        <form className="sign-in-methods__form" onSubmit={handleSubmit}>
          <label className="sign-in-methods__field">
            <span>{copy.stepUpPasswordLabel}</span>
            <input
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          {error && <AuthNotice variant="error">{error}</AuthNotice>}
          <div className="sign-in-methods__actions">
            <Button type="submit" variant="primary" disabled={submitting}>
              {submitting ? copy.stepUpConfirming : copy.stepUpConfirm}
            </Button>
          </div>
        </form>
      )}

      <ProviderButtons
        providers={providers}
        label={copy.continueWithProvider}
        onChoose={onProvider}
        disabled={submitting}
      />

      {!withPassword && providers.length === 0 && <p className="sign-in-methods__muted">{copy.stepUpNoWay}</p>}

      <div className="sign-in-methods__actions">
        <Button variant="ghost" onClick={onCancel} disabled={submitting}>
          {copy.cancel}
        </Button>
      </div>
    </section>
  );
}
