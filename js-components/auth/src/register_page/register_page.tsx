import { useState, type FormEvent, type MouseEvent } from "react";
import { Button } from "@slop-factory/ui";
import { useAuthPages, type ExtraValues, type LoginProvider } from "../config";
import AuthLayout, { AuthNotice } from "../auth_layout/auth_layout";
import ProviderButtons, { OrDivider } from "../provider_buttons/provider_buttons";
import { explainError } from "../error_codes";

export default function RegisterPage() {
  const {
    api,
    navigate,
    routes,
    nameFields,
    registrationOpen,
    passwordRules,
    copy,
    describeError,
    onError,
    errorCode,
    providers,
    redirect,
    signUpExtras,
  } = useAuthPages();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [names, setNames] = useState<Record<string, string>>(() =>
    Object.fromEntries(nameFields.map((field) => [field.name, ""])),
  );
  const [extras, setExtras] = useState<ExtraValues>({});
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  // The register call answers the same way whether or not the address is
  // already registered -- what differs is only the email sent, never the
  // response -- so success here always swaps to the same "check your
  // email" notice. Anything else would tell an attacker which addresses
  // already have accounts.
  const [submitted, setSubmitted] = useState(false);

  function goToSignIn(e: MouseEvent) {
    e.preventDefault();
    navigate(routes.login);
  }

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    // Not only the disabled button: a form can be submitted without it.
    if (!extrasComplete) return;
    setError(null);
    setSubmitting(true);
    try {
      await api.register(signUpExtras ? { email, password, names, extras } : { email, password, names });
      setSubmitted(true);
    } catch (err) {
      setError(describeError(err));
      onError?.("register", err);
    } finally {
      setSubmitting(false);
    }
  }

  const extrasComplete = signUpExtras ? signUpExtras.isComplete(extras) : true;
  const providerSignUp = api.startProviderSignUp;
  const offered = providerSignUp ? providers : [];

  async function continueWith(p: LoginProvider) {
    setError(null);
    try {
      redirect(await providerSignUp!(p.name, extras));
    } catch (err) {
      setError(explainError(err, { errorCode, describeError }, p.displayName));
      onError?.("register", err);
      throw err;
    }
  }

  // Showing the form and letting every submit fail is a worse account of
  // "sign-up is closed" than just saying so up front.
  if (!registrationOpen) {
    return (
      <AuthLayout title={copy.registrationClosedTitle}>
        <div className="auth-layout__body">
          <AuthNotice variant="info">{copy.registrationClosed}</AuthNotice>
          <p className="auth-layout__switch">
            <a href={routes.login} onClick={goToSignIn}>
              {copy.backToSignIn}
            </a>
          </p>
        </div>
      </AuthLayout>
    );
  }

  if (submitted) {
    return (
      <AuthLayout title="Check your email">
        <div className="auth-layout__body">
          <AuthNotice variant="success">{copy.registerSent(email)}</AuthNotice>
          <p className="auth-layout__switch">
            <a href={routes.login} onClick={goToSignIn}>
              {copy.backToSignIn}
            </a>
          </p>
        </div>
      </AuthLayout>
    );
  }

  return (
    <AuthLayout title={copy.registerTitle}>
      <form className="auth-layout__form" onSubmit={handleSubmit}>
        {offered.length > 0 && (
          <>
            <ProviderButtons
              providers={offered}
              label={copy.continueWithProvider}
              onChoose={continueWith}
              disabled={submitting || !extrasComplete}
            />
            <OrDivider label={copy.orDivider} />
          </>
        )}

        {nameFields.map((field) => (
          <label className="auth-layout__field" key={field.name}>
            <span>{field.label}</span>
            <input
              type="text"
              autoComplete={field.autoComplete}
              required
              minLength={field.minLength}
              maxLength={field.maxLength}
              value={names[field.name] ?? ""}
              onChange={(e) => setNames((prev) => ({ ...prev, [field.name]: e.target.value }))}
            />
          </label>
        ))}

        <label className="auth-layout__field">
          <span>Email</span>
          <input
            type="email"
            autoComplete="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </label>

        <label className="auth-layout__field">
          <span>Password</span>
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

        {signUpExtras && <signUpExtras.Fields values={extras} onChange={setExtras} disabled={submitting} />}

        {error && <AuthNotice variant="error">{error}</AuthNotice>}

        <Button
          type="submit"
          variant="primary"
          className="auth-layout__submit"
          disabled={submitting || !extrasComplete}
        >
          {submitting ? copy.creatingAccount : copy.createAccount}
        </Button>

        <p className="auth-layout__switch">
          {copy.alreadyHaveAccountPrompt}{" "}
          <a href={routes.login} onClick={goToSignIn}>
            {copy.signIn}
          </a>
        </p>
      </form>
    </AuthLayout>
  );
}
