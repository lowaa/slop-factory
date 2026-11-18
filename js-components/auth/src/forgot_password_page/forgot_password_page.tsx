import { useState, type FormEvent, type MouseEvent } from "react";
import { Button } from "@slop-factory/ui";
import { useAuthPages } from "../config";
import AuthLayout, { AuthNotice } from "../auth_layout/auth_layout";

export default function ForgotPasswordPage() {
  const { api, navigate, routes, copy, describeError, onError } = useAuthPages();
  const [email, setEmail] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  // The request answers the same way whether or not the address has an
  // account (enumeration protection) -- success here always swaps to the
  // same notice, regardless.
  const [submitted, setSubmitted] = useState(false);

  function goToSignIn(e: MouseEvent) {
    e.preventDefault();
    navigate(routes.login);
  }

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await api.requestPasswordReset(email);
      setSubmitted(true);
    } catch (err) {
      setError(describeError(err));
      onError?.("request-password-reset", err);
    } finally {
      setSubmitting(false);
    }
  }

  if (submitted) {
    return (
      <AuthLayout title="Check your email">
        <div className="auth-layout__body">
          <AuthNotice variant="success">{copy.resetLinkSent(email)}</AuthNotice>
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
    <AuthLayout title={copy.forgotPasswordTitle}>
      <form className="auth-layout__form" onSubmit={handleSubmit}>
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

        {error && <AuthNotice variant="error">{error}</AuthNotice>}

        <Button type="submit" variant="primary" className="auth-layout__submit" disabled={submitting}>
          {submitting ? copy.sendingResetLink : copy.sendResetLink}
        </Button>

        <p className="auth-layout__switch">
          <a href={routes.login} onClick={goToSignIn}>
            {copy.backToSignIn}
          </a>
        </p>
      </form>
    </AuthLayout>
  );
}
