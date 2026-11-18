import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient, ApiError, type GitHubAppWebhook } from "@/api";
import StepUpPrompt from "@/shared/step_up_prompt/step_up_prompt";
import CopyButton from "@/shared/copy_button/copy_button";
import Timestamp from "@/shared/timestamp/timestamp";
import { errorMessage } from "@/lib/use_async_action";

type StatusState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; data: GitHubAppWebhook };

type Action = "check" | "take_over" | "rotate";

const EVENT_LABELS: Record<string, string> = {
  pull_request: "Pull request",
  pull_request_review: "Pull request review",
};

interface Headline {
  tone: "ok" | "todo" | "off";
  text: string;
}

function headlineFor(webhook: GitHubAppWebhook): Headline {
  switch (webhook.state) {
    case "receiving":
      return { tone: "ok", text: "Receiving. Reviews and merges on pull requests reach the board through the GitHub App." };
    case "awaiting_events":
      return {
        tone: "todo",
        text: "Not done. The App's webhook is set, but no pull request event has arrived through it yet.",
      };
    case "url_differs":
      return { tone: "todo", text: "Not done. The URL set on GitHub is not this environment's." };
    case "not_applied":
      return { tone: "todo", text: "Not done. slop-haus has not set the webhook URL and secret on the App." };
    case "shared_installation":
      return {
        tone: "off",
        text: "Another workspace on this server already receives this installation's webhooks. GitHub keeps one secret per App, so this workspace receives none until that changes.",
      };
    case "unavailable":
      return webhook.expected_url
        ? { tone: "off", text: "Connect and install a GitHub App above to receive reviews and merges from GitHub." }
        : { tone: "off", text: "This server does not receive GitHub App webhooks." };
  }
}

function problemMessage(err: unknown, fallback: string): string {
  return errorMessage(err, fallback);
}

// AppWebhookStatus is whether the GitHub App's own webhook delivers reviews
// and merges, and what an owner has to do if it does not. GitHub does not let
// slop-haus change an existing App's subscribed events, so "receiving" is
// only claimed once a pull request event has actually arrived.
export default function AppWebhookStatus() {
  const [state, setState] = useState<StatusState>({ status: "loading" });
  const [busy, setBusy] = useState<Action | null>(null);
  const [error, setError] = useState<{ message: string; code?: string } | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [stepUp, setStepUp] = useState(false);

  // A slower, older fetch must not put stale data over a newer one.
  const issued = useRef(0);
  const applied = useRef(0);

  const fetchStatus = useCallback(() => {
    const request = ++issued.current;
    return apiClient
      .getGitHubAppWebhook()
      .then((data) => {
        if (request < applied.current) return;
        applied.current = request;
        setState({ status: "loaded", data });
      })
      .catch((err: unknown) => {
        if (request < applied.current) return;
        applied.current = request;
        setState({ status: "error", message: problemMessage(err, "Failed to load the GitHub App webhook.") });
      });
  }, []);

  useEffect(() => {
    void fetchStatus();
  }, [fetchStatus]);

  async function perform(action: Action) {
    setBusy(action);
    setError(null);
    setNotice(null);
    try {
      if (action === "rotate") {
        const data = await apiClient.rotateGitHubAppWebhookSecret();
        setState({ status: "loaded", data });
        setNotice("The webhook secret was replaced.");
      } else {
        const result = await apiClient.checkGitHubAppWebhook(action === "take_over");
        applied.current = ++issued.current;
        setState({ status: "loaded", data: result.webhook });
        setNotice(result.outcome === "applied" ? "GitHub's webhook was set to this environment's URL." : "GitHub's webhook already matches.");
      }
    } catch (err) {
      if (err instanceof ApiError && err.code === "reauth_required") {
        setStepUp(true);
      } else {
        setError({ message: problemMessage(err, "The check failed."), code: err instanceof ApiError ? err.code : undefined });
      }
    } finally {
      setBusy(null);
    }
  }

  function handleStepUpConfirmed() {
    setStepUp(false);
    void perform("rotate");
  }

  return (
    <section className="github-webhooks-panel__section" aria-label="GitHub App webhook">
      <h3>GitHub App webhook</h3>
      {state.status === "loading" && <p>Loading…</p>}
      {state.status === "error" && (
        <p role="alert" className="github-webhooks-panel__notice">
          {state.message}
        </p>
      )}
      {state.status === "loaded" && (
        <Loaded
          webhook={state.data}
          busy={busy}
          onAction={(action) => void perform(action)}
          error={error}
          notice={notice}
          stepUp={stepUp}
          onStepUpConfirmed={handleStepUpConfirmed}
          onStepUpCancel={() => setStepUp(false)}
        />
      )}
    </section>
  );
}

interface LoadedProps {
  webhook: GitHubAppWebhook;
  busy: Action | null;
  onAction: (action: Action) => void;
  error: { message: string; code?: string } | null;
  notice: string | null;
  stepUp: boolean;
  onStepUpConfirmed: () => void;
  onStepUpCancel: () => void;
}

function Loaded({ webhook, busy, onAction, error, notice, stepUp, onStepUpConfirmed, onStepUpCancel }: LoadedProps) {
  const headline = headlineFor(webhook);
  const configurable = webhook.state !== "unavailable";
  const canAct = webhook.can_edit && configurable && webhook.state !== "shared_installation";
  return (
    <>
      <p className={`github-webhooks-panel__app-state github-webhooks-panel__app-state--${headline.tone}`} role="status">
        {headline.text}
      </p>

      {webhook.state === "awaiting_events" && (
        <div className="github-webhooks-panel__instructions">
          <p>
            An App made here asks for the events itself; an App connected by hand, or made before this, may not be
            subscribed. On GitHub: <strong>Settings → Developer settings → GitHub Apps → your App</strong>. Under{" "}
            <strong>General → Webhook</strong> tick <strong>Active</strong>; under{" "}
            <strong>Permissions &amp; events → Subscribe to events</strong> tick{" "}
            {webhook.events.map((event, i) => (
              <span key={event}>
                {i > 0 && " and "}
                <strong>{EVENT_LABELS[event] ?? event}</strong>
              </span>
            ))}
            . This stays "not done" until one arrives.
          </p>
        </div>
      )}

      {configurable && (
        <dl className="github-webhooks-panel__app-facts">
          <dt>Expected URL</dt>
          <dd className="github-webhooks-panel__mono">
            {webhook.expected_url} <CopyButton value={webhook.expected_url} label="expected URL" />
          </dd>
          <dt>Set on GitHub</dt>
          <dd className="github-webhooks-panel__mono">{webhook.configured_url || "never"}</dd>
          <dt>Last delivery</dt>
          <dd>
            {webhook.last_delivery ? (
              <>
                <Timestamp at={webhook.last_delivery.received_at} /> {webhook.last_delivery.event}
                {webhook.last_delivery.action && `.${webhook.last_delivery.action}`}
              </>
            ) : (
              "never"
            )}
          </dd>
          <dt>Last pull request event</dt>
          <dd>{webhook.last_event ? <Timestamp at={webhook.last_event.received_at} /> : "never"}</dd>
        </dl>
      )}

      {notice && (
        <p role="status" className="github-webhooks-panel__notice">
          {notice}
        </p>
      )}
      {error && (
        <p role="alert" className="github-webhooks-panel__notice">
          {error.message}
        </p>
      )}
      {stepUp && <StepUpPrompt next="/git" onConfirmed={onStepUpConfirmed} onCancel={onStepUpCancel} />}

      {canAct && !stepUp && (
        <div className="github-webhooks-panel__actions">
          <button type="button" disabled={busy !== null} onClick={() => onAction("check")}>
            {busy === "check" ? "Checking…" : "Check"}
          </button>
          {error?.code === "github_app_webhook_foreign" && (
            <button type="button" disabled={busy !== null} onClick={() => onAction("take_over")}>
              {busy === "take_over" ? "Taking over…" : "Take over the webhook"}
            </button>
          )}
          <button type="button" disabled={busy !== null} onClick={() => onAction("rotate")}>
            {busy === "rotate" ? "Rotating…" : "Rotate secret"}
          </button>
        </div>
      )}
      {configurable && !webhook.can_edit && (
        <p className="github-webhooks-panel__empty">Only an owner can check or rotate the App's webhook.</p>
      )}
    </>
  );
}
