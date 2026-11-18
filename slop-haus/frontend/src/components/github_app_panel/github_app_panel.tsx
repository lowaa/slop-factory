import { useState, type FormEvent } from "react";
import { Button } from "@slop-factory/ui";
import {
  apiClient,
  ApiError,
  type GitHubAppVerification,
} from "@/api";
import StepUpPrompt from "@/shared/step_up_prompt/step_up_prompt";
import { errorMessage, useAsyncAction } from "@/lib/use_async_action";
import { usePolledResource } from "@/lib/use_polled_resource";
import { postFormTo, redirectBrowserTo } from "@/lib/navigation";
import Timestamp from "@/shared/timestamp/timestamp";
import "./github_app_panel.scss";

interface FormValues {
  appId: string;
  installationId: string;
  privateKey: string;
}

// What replacing a working connection costs, said beside the Create button and
// again in the confirmation: the new App is stored as soon as GitHub creates
// it, and a connection with no installation is not a working one.
const REPLACE_WARNING =
  "The current connection stops working as soon as GitHub creates the new App, and stays down until the new App is installed, which in an organisation includes waiting for an owner to approve it. Agents cannot open or merge pull requests meanwhile. The old App is not deleted from GitHub; delete it there by hand.";

const EMPTY_FORM: FormValues = {
  appId: "",
  installationId: "",
  privateKey: "",
};

type Action =
  | { kind: "connect" }
  | { kind: "remove" }
  | { kind: "create" }
  | { kind: "install" };

function parseId(text: string): number | null {
  const trimmed = text.trim();
  return /^[1-9][0-9]*$/.test(trimmed) ? Number(trimmed) : null;
}

function shortFingerprint(fingerprint: string): string {
  return fingerprint.slice(0, 16);
}

// GitHubAppPanel is where a tenant's owners connect the GitHub App slop-haus
// acts through. The private key is write-only: nothing the server answers
// carries it, so the form clears it once it is stored and the panel only ever
// shows the key's fingerprint. Authorisation is the
// server's: can_edit only decides which controls to show.
export default function GitHubAppPanel() {
  const status = usePolledResource(() => apiClient.getGitHubApp(), {
    fallbackError: "Failed to load the GitHub App connection.",
  });
  const { busy, error, setError, run } = useAsyncAction();
  const [form, setForm] = useState<FormValues>(EMPTY_FORM);
  const [replacing, setReplacing] = useState(false);
  const [verification, setVerification] =
    useState<GitHubAppVerification | null>(null);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [stepUp, setStepUp] = useState<Action | null>(null);
  const [account, setAccount] = useState("");
  const [confirmCreate, setConfirmCreate] = useState(false);

  function perform(action: Action) {
    return run(
      async () => {
        if (action.kind === "create") {
          const start = await apiClient.beginGitHubAppManifest(account.trim());
          // GitHub's page takes the manifest as a posted form, and returns here
          // with a code (see github_app_callback.tsx). The page is left on
          // purpose; nothing below runs for it.
          postFormTo(start.action_url, { manifest: start.manifest });
          return;
        }
        if (action.kind === "install") {
          redirectBrowserTo(await apiClient.getGitHubAppInstallURL());
          return;
        }
        if (action.kind === "connect") {
          const appId = parseId(form.appId);
          const installationId = parseId(form.installationId);
          if (appId === null || installationId === null) {
            setError("The App id and the installation id are whole numbers.");
            return;
          }
          await apiClient.connectGitHubApp({
            app_id: appId,
            installation_id: installationId,
            private_key: form.privateKey,
          });
          // Cleared once it is stored, and never put back: no answer carries it.
          setForm(EMPTY_FORM);
          setReplacing(false);
        } else {
          await apiClient.removeGitHubApp();
          setConfirmRemove(false);
        }
        setVerification(null);
        await status.reload();
      },
      (err) => {
        if (err instanceof ApiError && err.code === "reauth_required") {
          setStepUp(action);
          return null;
        }
        return errorMessage(err, "That did not work. Try again.");
      },
    );
  }

  function handleVerify() {
    return run(
      async () => {
        setVerification(await apiClient.verifyGitHubApp());
        await status.reload();
      },
      (err) => {
        setVerification(null);
        return errorMessage(err, "Could not verify the connection.");
      },
    );
  }

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    void perform({ kind: "connect" });
  }

  function handleStepUpConfirmed() {
    const action = stepUp;
    setStepUp(null);
    if (action) void perform(action);
  }

  const data = status.status === "ready" ? (status.data ?? null) : null;
  const credential = data?.credential ?? null;
  const canEdit = data?.can_edit ?? false;
  const showForm =
    data !== null &&
    data.enabled &&
    canEdit &&
    (credential === null || replacing);

  return (
    <section className="github-app-panel" aria-label="GitHub App">
      <h2 className="github-app-panel__heading">GitHub App</h2>
      <p className="github-app-panel__hint">
        The App slop-haus acts through on this team&rsquo;s repositories. Create
        it here and GitHub asks you to install it on the repositories you
        choose. In an organisation, an owner of the organisation will be asked
        to approve the install; that is GitHub&rsquo;s own rule for any App, not
        something slop-haus adds. See docs/github_app.md.
      </p>

      {status.status === "loading" && <p>Loading…</p>}
      {status.status === "error" && (
        <p role="alert" className="github-app-panel__error">
          {status.error}
        </p>
      )}
      {error && (
        <p role="alert" className="github-app-panel__error">
          {error}
        </p>
      )}

      {data && !data.enabled && (
        <p className="github-app-panel__notice" role="status">
          This server has no CREDENTIAL_ENCRYPTION_KEY, so it keeps no GitHub
          App credential.
        </p>
      )}

      {data && data.enabled && credential === null && (
        <p>No GitHub App is connected.</p>
      )}

      {credential && (
        <dl className="github-app-panel__facts">
          <dt>App id</dt>
          <dd>{credential.app_id}</dd>
          <dt>Installation id</dt>
          <dd>
            {credential.pending
              ? "not installed yet"
              : credential.installation_id}
          </dd>
          <dt>Key fingerprint</dt>
          <dd>
            <code>{shortFingerprint(credential.key_fingerprint)}</code>
          </dd>
          <dt>Verified</dt>
          <dd>
            {credential.verified_at ? (
              <Timestamp at={credential.verified_at} />
            ) : (
              "not yet"
            )}
          </dd>
        </dl>
      )}

      {verification && (
        <div className="github-app-panel__verification" role="status">
          <p>
            Connected as an installation on{" "}
            <strong>{verification.account}</strong>, which can see{" "}
            {verification.repos.length}{" "}
            {verification.repos.length === 1 ? "repository" : "repositories"}.
          </p>
          {verification.manifest.length === 0 ? (
            <p className="github-app-panel__hint">
              No GitHub repositories in the workspace manifest to check.
            </p>
          ) : (
            <ul>
              {verification.manifest.map((entry) => (
                <li key={entry.remote}>
                  <code>{entry.remote}</code>{" "}
                  <span
                    className={`github-app-panel__visible github-app-panel__visible--${entry.visible ? "yes" : "no"}`}
                  >
                    {entry.visible
                      ? "visible to the installation"
                      : "NOT visible to the installation"}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {stepUp && (
        <StepUpPrompt
          next="/git"
          onConfirmed={handleStepUpConfirmed}
          onCancel={() => setStepUp(null)}
        />
      )}

      {credential?.pending && !replacing && (
        <p className="github-app-panel__notice" role="status">
          The App is created but not installed. Install it to choose its
          repositories. If an org owner has to approve the install, it is
          waiting for them: press Verify once they have, and the board picks it
          up.
        </p>
      )}

      {showForm && !stepUp && (
        <div className="github-app-panel__create">
          <label>
            GitHub organisation that should own the App (leave empty for your
            own account)
            <input
              value={account}
              maxLength={39}
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => setAccount(e.target.value)}
            />
          </label>
          {credential && (
            <p className="github-app-panel__warning" role="note">
              {REPLACE_WARNING}
            </p>
          )}
          <div className="github-app-panel__actions">
            <Button
              variant="primary"
              disabled={busy}
              onClick={() =>
                credential
                  ? setConfirmCreate(true)
                  : void perform({ kind: "create" })
              }
            >
              Create GitHub App
            </Button>
          </div>
        </div>
      )}

      {confirmCreate && (
        <div className="github-app-panel__backdrop">
          <div
            role="dialog"
            aria-modal="true"
            aria-labelledby="github-app-replace-title"
            className="github-app-panel__dialog"
          >
            <h4 id="github-app-replace-title">Replace the working connection?</h4>
            <p>{REPLACE_WARNING}</p>
            <div className="github-app-panel__actions">
              <Button
                variant="danger"
                disabled={busy}
                onClick={() => {
                  setConfirmCreate(false);
                  void perform({ kind: "create" });
                }}
              >
                Replace and create the App
              </Button>
              <Button disabled={busy} onClick={() => setConfirmCreate(false)}>
                Keep the current connection
              </Button>
            </div>
          </div>
        </div>
      )}

      {showForm && !stepUp && (
        <details className="github-app-panel__manual">
          <summary>Connect an App that already exists, by hand</summary>
          <form className="github-app-panel__form" onSubmit={handleSubmit}>
            <label>
              App id
              <input
                inputMode="numeric"
                value={form.appId}
                onChange={(e) => setForm({ ...form, appId: e.target.value })}
              />
            </label>
            <label>
              Installation id
              <input
                inputMode="numeric"
                value={form.installationId}
                onChange={(e) =>
                  setForm({ ...form, installationId: e.target.value })
                }
              />
            </label>
            <label>
              Private key (PEM)
              <textarea
                rows={6}
                spellCheck={false}
                autoComplete="off"
                placeholder="-----BEGIN RSA PRIVATE KEY-----"
                value={form.privateKey}
                onChange={(e) =>
                  setForm({ ...form, privateKey: e.target.value })
                }
              />
            </label>
            <p className="github-app-panel__hint">
              The key is encrypted before it is stored and cannot be shown
              again; to change it, connect again.
            </p>
            <div className="github-app-panel__actions">
              <Button type="submit" variant="primary" disabled={busy}>
                {credential ? "Replace" : "Connect"}
              </Button>
              {credential && (
                <Button
                  disabled={busy}
                  onClick={() => {
                    setReplacing(false);
                    setForm(EMPTY_FORM);
                  }}
                >
                  Cancel
                </Button>
              )}
            </div>
          </form>
        </details>
      )}

      {credential && canEdit && !replacing && !stepUp && (
        <div className="github-app-panel__actions">
          {credential.pending && credential.app_slug && (
            <Button
              variant="primary"
              disabled={busy}
              onClick={() => void perform({ kind: "install" })}
            >
              Install the App
            </Button>
          )}
          <Button disabled={busy} onClick={() => void handleVerify()}>
            Verify
          </Button>
          <Button disabled={busy} onClick={() => setReplacing(true)}>
            Replace
          </Button>
          {confirmRemove ? (
            <>
              <span>
                Remove the connection? Tokens already issued run out within the
                hour.
              </span>
              <Button
                variant="danger"
                disabled={busy}
                onClick={() => void perform({ kind: "remove" })}
              >
                Confirm remove
              </Button>
              <Button disabled={busy} onClick={() => setConfirmRemove(false)}>
                Keep
              </Button>
            </>
          ) : (
            <Button
              variant="danger"
              disabled={busy}
              onClick={() => setConfirmRemove(true)}
            >
              Remove
            </Button>
          )}
        </div>
      )}

      {data && !canEdit && (
        <p className="github-app-panel__hint">
          Only the tenant&rsquo;s owners can change this.
        </p>
      )}
    </section>
  );
}
