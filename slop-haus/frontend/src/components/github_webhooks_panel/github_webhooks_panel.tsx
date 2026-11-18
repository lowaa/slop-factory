import { useEffect, useState } from "react";
import { apiClient, type GitHubDelivery, type GitHubWebhooks } from "@/api";
import { Link } from "@slop-factory/router";
import Timestamp from "@/shared/timestamp/timestamp";
import TicketLink from "@/shared/ticket_link/ticket_link";
import { ACCOUNT_PATH } from "@/components/account_page";
import AppWebhookStatus from "./app_webhook_status";
import "./github_webhooks_panel.scss";
import { errorMessage } from "@/lib/use_async_action";

type WebhooksState = { status: "loading" } | { status: "error"; message: string } | { status: "loaded"; data: GitHubWebhooks };

function deliveryLabel(delivery: GitHubDelivery): string {
  return delivery.action ? `${delivery.event}.${delivery.action}` : delivery.event;
}

export default function GitHubWebhooksPanel() {
  const [state, setState] = useState<WebhooksState>({ status: "loading" });

  useEffect(() => {
    let cancelled = false;
    apiClient
      .listGitHubWebhooks()
      .then((data) => {
        if (!cancelled) setState({ status: "loaded", data });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setState({
          status: "error",
          message: errorMessage(err, "Failed to load GitHub webhooks."),
        });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const enabled = state.status === "loaded" ? state.data.enabled : true;

  return (
    <div className="github-webhooks-panel">
      <AppWebhookStatus />
      <section className="github-webhooks-panel__section">
        <h2>GitHub webhooks</h2>
        <div className="github-webhooks-panel__instructions">
          <p>
            An approval or request for changes on a ticket's PR moves the ticket the same way the board's Approve /
            Request changes buttons would; merging it yourself on GitHub closes a ticket in review. Only reviews by
            members who have linked their GitHub account (
            <Link to={ACCOUNT_PATH}>Account → Sign-in methods</Link>) count -- reviews by the agents' own bot account
            are ignored.
          </p>
        </div>

        {!enabled && (
          <p className="github-webhooks-panel__notice" role="status">
            GitHub webhooks are not configured on this server.
          </p>
        )}
      </section>

      <section className="github-webhooks-panel__section">
        <h3>Recent deliveries</h3>
        {state.status === "loading" && <p>Loading…</p>}
        {state.status === "error" && (
          <p role="alert" className="github-webhooks-panel__notice">
            {state.message}
          </p>
        )}
        {state.status === "loaded" && (
          <>
            <ul className="github-webhooks-panel__deliveries">
              {state.data.deliveries.map((delivery) => (
                <li key={delivery.id} className="github-webhooks-panel__delivery">
                  <Timestamp at={delivery.received_at} />
                  <span className="github-webhooks-panel__event">{deliveryLabel(delivery)}</span>
                  <span className={`github-webhooks-panel__outcome github-webhooks-panel__outcome--${delivery.outcome}`}>
                    {delivery.outcome}
                  </span>
                  {delivery.ticket_id !== null && (
                    <TicketLink ticketId={delivery.ticket_id} from="git">
                      #{delivery.ticket_id}
                    </TicketLink>
                  )}
                  <span className="github-webhooks-panel__detail">{delivery.detail}</span>
                </li>
              ))}
            </ul>
            {state.data.deliveries.length === 0 && <p className="github-webhooks-panel__empty">No deliveries yet.</p>}
          </>
        )}
      </section>
    </div>
  );
}
