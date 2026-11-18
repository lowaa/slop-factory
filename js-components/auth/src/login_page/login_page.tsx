import { useState, type FormEvent } from "react";
import { Button } from "@slop-factory/ui";
import { useAuthPages } from "../config";
import AuthLayout, { AuthNotice } from "../auth_layout/auth_layout";
import ProviderButtons, { OrDivider } from "../provider_buttons/provider_buttons";
import { explainCallbackError, explainError } from "../error_codes";

interface LoginPageProps {
  // Set when the host app dropped the person here mid-use (an expired or
  // revoked session) -- distinguishes "your session ended" from a plain
  // first visit, without every other route having to thread the message
  // through as a prop.
  notice?: string | null;
  // The code a refused provider sign-in came back with (see
  // readAndClearCallbackError), shown as the page's error.
  callbackError?: string | null;
}

export default function LoginPage({ notice, callbackError }: LoginPageProps) {
  const {
    api,
    navigate,
    routes,
    registrationOpen,
    copy,
    describeError,
    errorCode,
    onError,
    providers,
    providerSignInUrl,
    passwordLogin,
    passwordReset,
    redirect,
    signInErrorExtra,
  } = useAuthPages();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(() =>
    callbackError ? explainCallbackError(callbackError, providers) : null,
  );
  // The code behind the error shown, for signInErrorExtra.
  const [shownCode, setShownCode] = useState<string | undefined>(callbackError ?? undefined);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    setShownCode(undefined);
    setSubmitting(true);
    try {
      // The host's config.api.signIn is responsible for flipping its own
      // auth state to signed-in on success -- no navigation call needed
      // here, the host's root view switches views on its own.
      await api.signIn(email, password);
    } catch (err) {
      setError(explainError(err, { errorCode, describeError }));
      setShownCode(errorCode?.(err));
      onError?.("sign-in", err);
    } finally {
      setSubmitting(false);
    }
  }

  const providerButtons = (
    <ProviderButtons
      providers={providers}
      label={copy.continueWithProvider}
      onChoose={(p) => {
        if (!providerSignInUrl) throw new Error("AuthPagesConfig.providers needs providerSignInUrl");
        redirect(providerSignInUrl(p.name));
      }}
    />
  );

  if (!passwordLogin) {
    return (
      <AuthLayout title={copy.signIn}>
        <div className="auth-layout__body">
          {!error && notice && <AuthNotice variant="info">{notice}</AuthNotice>}
          {error && <AuthNotice variant="error">{error}</AuthNotice>}
          {error && signInErrorExtra?.(shownCode)}
          {providerButtons}
        </div>
      </AuthLayout>
    );
  }

  return (
    <AuthLayout title={copy.signIn}>
      <form className="auth-layout__form" onSubmit={handleSubmit}>
        {!error && notice && <AuthNotice variant="info">{notice}</AuthNotice>}

        {providers.length > 0 && (
          <>
            {providerButtons}
            <OrDivider label={copy.orDivider} />
          </>
        )}

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
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>

        {error && <AuthNotice variant="error">{error}</AuthNotice>}
        {error && signInErrorExtra?.(shownCode)}

        <Button type="submit" variant="primary" className="auth-layout__submit" disabled={submitting}>
          {submitting ? copy.signingIn : copy.signIn}
        </Button>

        {passwordReset && (
          <p className="auth-layout__switch">
            <a
              href={routes.forgotPassword}
              onClick={(e) => {
                e.preventDefault();
                navigate(routes.forgotPassword);
              }}
            >
              {copy.forgotPasswordLink}
            </a>
          </p>
        )}

        {/* Sign-up closed (an invitation-only deployment) means there is
            nowhere for this link to lead -- the register call would just
            fail -- so it's dropped rather than shown and left to fail. */}
        {registrationOpen && (
          <p className="auth-layout__switch">
            {copy.noAccountPrompt}{" "}
            <a
              href={routes.register}
              onClick={(e) => {
                e.preventDefault();
                navigate(routes.register);
              }}
            >
              {copy.signUpLink}
            </a>
          </p>
        )}
      </form>
    </AuthLayout>
  );
}
