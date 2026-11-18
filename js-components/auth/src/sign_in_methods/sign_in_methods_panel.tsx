import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Button } from "@slop-factory/ui";
import type { LoginProvider, PasswordRules } from "../config";
import { AuthNotice, type AuthNoticeVariant } from "../auth_layout/auth_layout";
import { explainCallbackError, explainError } from "../error_codes";
import { redirectTo } from "../navigation";
import { defaultSignInMethodsCopy, type SignInMethodsCopy } from "./copy";
import { clearPendingStepUp, peekPendingStepUp, stashPendingStepUp, type StoredAction } from "./pending_step_up";
import StepUpPrompt from "./step_up_prompt";
import { stepUpMethodsOf } from "./step_up_methods";
import type { IdentityMethod, MethodAction, SignInMethods, SignInMethodsApi } from "./types";
import "./sign_in_methods_panel.scss";

export interface SignInMethodsPanelProps {
  api: SignInMethodsApi;
  // The providers configured here: offered for linking, and for a step-up
  // where the account has an identity at one.
  providers: LoginProvider[];
  // The page this panel is on, where a link or step-up through a provider
  // comes back to.
  returnPath: string;
  // The code a refused link or step-up came back with (?error=<code>; see
  // readAndClearCallbackError).
  callbackError?: string | null;
  // Adding a password also asks for the address it signs in with -- an app
  // whose accounts have no address of their own; otherwise
  // the account's address is the password's.
  addPasswordAsksEmail?: boolean;
  passwordRules?: PasswordRules;
  errorCode: (err: unknown) => string | undefined;
  describeError: (err: unknown) => string;
  redirect?: (url: string) => void;
  copy?: Partial<SignInMethodsCopy>;
}

type Load = { status: "loading" } | { status: "error"; message: string } | { status: "ready"; methods: SignInMethods };

type Notice = { variant: AuthNoticeVariant; text: string } | null;

type OpenForm = "add-password" | "change-password" | null;

// A change stashed before a step-up through a provider, offered once on the
// way back. Password changes come back as a form to re-type into instead.
type Resume = Exclude<StoredAction, { kind: "add-password" } | { kind: "change-password" }>;

// SignInMethodsPanel lists an account's ways of signing in -- a password,
// identities at login providers -- and changes them: add or change the
// password, remove one, link a provider. Every change may need the person
// to confirm it's them first (reauth_required); the panel asks, then
// retries the change they made.
export default function SignInMethodsPanel({
  api,
  providers,
  returnPath,
  callbackError,
  addPasswordAsksEmail = false,
  passwordRules,
  errorCode,
  describeError,
  redirect = redirectTo,
  copy: copyOverrides,
}: SignInMethodsPanelProps) {
  const copy = { ...defaultSignInMethodsCopy, ...copyOverrides };
  const [load, setLoad] = useState<Load>({ status: "loading" });
  // What a step-up through a provider was for, read once on the way back.
  // The initializer only peeks -- it must not clear the stash itself, since
  // StrictMode calls it twice on mount and a combined read-and-clear there
  // would find nothing on the second call, losing the resume (see
  // pending_step_up.ts). The effect below clears it exactly once instead. A
  // refused step-up (callbackError) drops it rather than offering it again.
  const [pending] = useState<StoredAction | null>(() => (callbackError ? null : peekPendingStepUp()));
  useEffect(() => {
    clearPendingStepUp();
  }, []);
  const retype = pending?.kind === "add-password" || pending?.kind === "change-password" ? pending.kind : null;
  const [resume, setResume] = useState<Resume | null>(pending && !retype ? (pending as Resume) : null);
  const [form, setForm] = useState<OpenForm>(retype);
  const [notice, setNotice] = useState<Notice>(
    callbackError
      ? { variant: "error", text: explainCallbackError(callbackError, providers) }
      : retype
        ? { variant: "info", text: copy.resumeRetypePassword }
        : null,
  );
  const [confirming, setConfirming] = useState<string | null>(null);
  const [stepUp, setStepUp] = useState<MethodAction | null>(null);
  const [busy, setBusy] = useState(false);

  const displayName = useCallback(
    (provider: string) => providers.find((p) => p.name === provider)?.displayName ?? provider,
    [providers],
  );

  const reload = useCallback(async () => {
    try {
      const methods = await api.list();
      setLoad({ status: "ready", methods });
      return methods;
    } catch (err) {
      setLoad({ status: "error", message: describeError(err) });
      return null;
    }
  }, [api, describeError]);

  useEffect(() => {
    void reload();
  }, [reload]);

  async function perform(action: MethodAction): Promise<void> {
    switch (action.kind) {
      case "add-password":
        return api.addPassword({ email: action.email, password: action.password });
      case "change-password":
        return api.changePassword(action.password);
      case "remove-password":
        return api.removePassword();
      case "remove-identity":
        return api.removeIdentity({ provider: action.provider, subject: action.subject });
      case "link":
        redirect(await api.startLink(action.provider, returnPath));
        return;
    }
  }

  function succeeded(action: MethodAction, methods: SignInMethods | null): string {
    switch (action.kind) {
      case "add-password":
        return methods?.password && !methods.password.verified && methods.password.email
          ? copy.passwordAddedUnverified(methods.password.email)
          : copy.passwordAdded;
      case "change-password":
        return copy.passwordChanged;
      case "remove-password":
        return copy.passwordRemoved;
      case "remove-identity":
        return copy.identityRemoved(displayName(action.provider));
      case "link":
        return "";
    }
  }

  // run makes a change. A refusal for want of a recent sign-in opens the
  // step-up prompt with the change kept, to retry once confirmed; any other
  // refusal is shown. Throws nothing: the step-up prompt retries through it
  // and learns the outcome from the state it leaves.
  async function run(action: MethodAction): Promise<void> {
    setBusy(true);
    setNotice(null);
    try {
      await perform(action);
      setStepUp(null);
      if (action.kind === "link") return; // the browser is on its way to the provider
      setForm(null);
      setConfirming(null);
      const methods = await reload();
      setNotice({ variant: "success", text: succeeded(action, methods) });
    } catch (err) {
      if (errorCode(err) === "reauth_required") {
        setStepUp(action);
      } else {
        setStepUp(null);
        setNotice({ variant: "error", text: explainError(err, { errorCode, describeError }, providerOf(action)) });
      }
    } finally {
      setBusy(false);
    }
  }

  function providerOf(action: MethodAction): string | undefined {
    return action.kind === "link" || action.kind === "remove-identity" ? displayName(action.provider) : undefined;
  }

  if (load.status === "loading") {
    return (
      <section className="sign-in-methods" aria-labelledby="sign-in-methods-title">
        <h2 id="sign-in-methods-title" className="sign-in-methods__title">
          {copy.title}
        </h2>
        <p className="sign-in-methods__muted">{copy.loading}</p>
      </section>
    );
  }

  if (load.status === "error") {
    return (
      <section className="sign-in-methods" aria-labelledby="sign-in-methods-title">
        <h2 id="sign-in-methods-title" className="sign-in-methods__title">
          {copy.title}
        </h2>
        <AuthNotice variant="error">
          {copy.loadFailed} {load.message}
        </AuthNotice>
        <div className="sign-in-methods__actions">
          <Button onClick={() => void reload()}>{copy.retry}</Button>
        </div>
      </section>
    );
  }

  const { methods } = load;
  const linkedProviders = new Set(methods.identities.map((i) => i.provider));
  const linkable = providers.filter((p) => !linkedProviders.has(p.name));
  const offered = stepUpMethodsOf(methods, providers);
  const showPasswordRow = methods.password !== null || methods.passwordLoginEnabled;

  return (
    <section className="sign-in-methods" aria-labelledby="sign-in-methods-title">
      <h2 id="sign-in-methods-title" className="sign-in-methods__title">
        {copy.title}
      </h2>
      <p className="sign-in-methods__muted">{copy.intro}</p>

      {notice && notice.text && <AuthNotice variant={notice.variant}>{notice.text}</AuthNotice>}

      {resume && (
        <div className="sign-in-methods__resume">
          <AuthNotice variant="info">{copy.resumed}</AuthNotice>
          <div className="sign-in-methods__actions">
            <Button
              variant="primary"
              disabled={busy}
              onClick={() => {
                const action = resume;
                setResume(null);
                void run(action);
              }}
            >
              {copy.resumeContinue}: {describeResume(resume, copy, displayName)}
            </Button>
            <Button variant="ghost" onClick={() => setResume(null)}>
              {copy.cancel}
            </Button>
          </div>
        </div>
      )}

      {stepUp && (
        <StepUpPrompt
          copy={copy}
          withPassword={offered.withPassword}
          providers={offered.providers}
          onPassword={async (password) => {
            await api.reauthWithPassword(password);
            await run(stepUp);
          }}
          onProvider={async (p) => {
            try {
              const url = await api.startReauth(p.name, returnPath);
              // Only once the round trip has started: a refused start
              // leaves nothing to offer again on a later visit.
              stashPendingStepUp(stepUp);
              redirect(url);
            } catch (err) {
              setNotice({ variant: "error", text: explainError(err, { errorCode, describeError }, p.displayName) });
              throw err;
            }
          }}
          onCancel={() => setStepUp(null)}
          describePasswordError={(err) =>
            errorCode(err) === "invalid_credentials"
              ? copy.stepUpWrongPassword
              : explainError(err, { errorCode, describeError })
          }
        />
      )}

      <ul className="sign-in-methods__list">
        {showPasswordRow && (
          <li className="sign-in-methods__item">
            <div className="sign-in-methods__row">
              <div className="sign-in-methods__what">
                <span className="sign-in-methods__name">{copy.password}</span>
                <span className="sign-in-methods__detail">{passwordDetail(methods, copy)}</span>
              </div>
              <div className="sign-in-methods__actions">
                {methods.password ? (
                  <>
                    <Button size="sm" disabled={busy} onClick={() => setForm("change-password")}>
                      {copy.changePassword}
                    </Button>
                    <Button
                      size="sm"
                      variant="danger"
                      disabled={busy}
                      aria-label={copy.removePasswordLabel}
                      onClick={() => setConfirming("password")}
                    >
                      {copy.remove}
                    </Button>
                  </>
                ) : (
                  <Button size="sm" disabled={busy} onClick={() => setForm("add-password")}>
                    {copy.addPassword}
                  </Button>
                )}
              </div>
            </div>
            {confirming === "password" && (
              <ConfirmRemove
                copy={copy}
                what={copy.password.toLowerCase()}
                busy={busy}
                onConfirm={() => void run({ kind: "remove-password" })}
                onCancel={() => setConfirming(null)}
              />
            )}
            {form && (
              <PasswordForm
                key={form}
                copy={copy}
                asksEmail={form === "add-password" && addPasswordAsksEmail}
                passwordRules={passwordRules}
                busy={busy}
                onSubmit={(email, password) =>
                  run(
                    form === "add-password"
                      ? { kind: "add-password", email, password }
                      : { kind: "change-password", password },
                  )
                }
                onCancel={() => setForm(null)}
              />
            )}
          </li>
        )}

        {methods.identities.map((identity) => {
          const key = identityKey(identity);
          const name = displayName(identity.provider);
          return (
            <li key={key} className="sign-in-methods__item">
              <div className="sign-in-methods__row">
                <div className="sign-in-methods__what">
                  <span className="sign-in-methods__name">{name}</span>
                  <span className="sign-in-methods__detail">
                    {identity.login}
                    {!identity.usable && ` (${copy.identityUnusable})`}
                  </span>
                </div>
                <div className="sign-in-methods__actions">
                  <Button
                    size="sm"
                    variant="danger"
                    disabled={busy}
                    aria-label={copy.removeIdentityLabel(name)}
                    onClick={() => setConfirming(key)}
                  >
                    {copy.remove}
                  </Button>
                </div>
              </div>
              {confirming === key && (
                <ConfirmRemove
                  copy={copy}
                  what={`${name} (${identity.login})`}
                  busy={busy}
                  onConfirm={() =>
                    void run({ kind: "remove-identity", provider: identity.provider, subject: identity.subject })
                  }
                  onCancel={() => setConfirming(null)}
                />
              )}
            </li>
          );
        })}
      </ul>

      {!showPasswordRow && <p className="sign-in-methods__muted">{copy.passwordLoginOff}</p>}

      {linkable.length > 0 && (
        <div className="sign-in-methods__actions">
          {linkable.map((p) => (
            <Button key={p.name} disabled={busy} onClick={() => void run({ kind: "link", provider: p.name })}>
              {copy.linkProvider(p.displayName)}
            </Button>
          ))}
        </div>
      )}
    </section>
  );
}

function identityKey(identity: IdentityMethod): string {
  return `${identity.provider}\u0000${identity.subject}`;
}

function passwordDetail(methods: SignInMethods, copy: SignInMethodsCopy): string {
  const { password } = methods;
  if (!password) return copy.passwordNotSet;
  const parts = [password.email, !password.verified ? copy.passwordUnverified : undefined];
  if (!methods.passwordLoginEnabled) parts.push(copy.identityUnusable);
  return parts.filter(Boolean).join(" — ") || "••••••••";
}

function describeResume(action: Resume, copy: SignInMethodsCopy, displayName: (p: string) => string): string {
  switch (action.kind) {
    case "link":
      return copy.linkProvider(displayName(action.provider));
    case "remove-password":
      return copy.removePasswordLabel;
    case "remove-identity":
      return copy.removeIdentityLabel(displayName(action.provider));
  }
}

interface ConfirmRemoveProps {
  copy: SignInMethodsCopy;
  what: string;
  busy: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

// A removal is confirmed inline: it's the one change here that can lock a
// person out of a way in, so one misclick shouldn't do it.
function ConfirmRemove({ copy, what, busy, onConfirm, onCancel }: ConfirmRemoveProps) {
  return (
    <div className="sign-in-methods__confirm" role="group" aria-label={copy.confirmRemove(what)}>
      <p className="sign-in-methods__muted">{copy.confirmRemove(what)}</p>
      <div className="sign-in-methods__actions">
        <Button size="sm" variant="danger" disabled={busy} onClick={onConfirm}>
          {copy.confirmRemoveButton}
        </Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={onCancel}>
          {copy.cancel}
        </Button>
      </div>
    </div>
  );
}

interface PasswordFormProps {
  copy: SignInMethodsCopy;
  asksEmail: boolean;
  passwordRules?: PasswordRules;
  busy: boolean;
  onSubmit: (email: string, password: string) => Promise<void>;
  onCancel: () => void;
}

function PasswordForm({ copy, asksEmail, passwordRules, busy, onSubmit, onCancel }: PasswordFormProps) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    void onSubmit(email, password);
  }

  return (
    <form className="sign-in-methods__form" onSubmit={handleSubmit}>
      {asksEmail && (
        <label className="sign-in-methods__field">
          <span>{copy.emailLabel}</span>
          <input type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
        </label>
      )}
      <label className="sign-in-methods__field">
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
      <div className="sign-in-methods__actions">
        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? copy.saving : copy.save}
        </Button>
        <Button variant="ghost" disabled={busy} onClick={onCancel}>
          {copy.cancel}
        </Button>
      </div>
    </form>
  );
}
