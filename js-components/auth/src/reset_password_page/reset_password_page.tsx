import { useState, type FormEvent } from "react";
import { Button } from "@slop-factory/ui";
import { useAuthPages } from "../config";
import AuthLayout, { AuthNotice } from "../auth_layout/auth_layout";
import { readAndClearToken } from "../token";

// Same fragment-token handling as ActivatePage. Unlike activation there's
// no request to de-duplicate here -- the token is only used when the
// person submits the form, not automatically on mount -- so a plain lazy
// useState initializer is enough; no StrictMode double-invoke guard
// needed.
export default function ResetPasswordPage() {
  const { api, navigate, routes, passwordRules, copy, describeError, onError } = useAuthPages();
  const [token] = useState<string | null>(readAndClearToken);
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [success, setSuccess] = useState(false);

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!token) return;
    setError(null);
    setSubmitting(true);
    try {
      await api.resetPassword(token, password);
      setSuccess(true);
    } catch (err) {
      setError(describeError(err));
      onError?.("reset-password", err);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <AuthLayout title={copy.resetPasswordTitle}>
      {!token && <AuthNotice variant="error">{copy.missingResetToken}</AuthNotice>}

      {token && success && (
        <div className="auth-layout__body">
          <AuthNotice variant="success">{copy.passwordChanged}</AuthNotice>
          <p className="auth-layout__switch">
            <a
              href={routes.login}
              onClick={(e) => {
                e.preventDefault();
                navigate(routes.login);
              }}
            >
              {copy.continueToSignIn}
            </a>
          </p>
        </div>
      )}

      {token && !success && (
        <form className="auth-layout__form" onSubmit={handleSubmit}>
          <label className="auth-layout__field">
            <span>{copy.newPasswordLabel}</span>
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

          <Button type="submit" variant="primary" className="auth-layout__submit" disabled={submitting}>
            {submitting ? copy.settingNewPassword : copy.setNewPassword}
          </Button>
        </form>
      )}
    </AuthLayout>
  );
}
