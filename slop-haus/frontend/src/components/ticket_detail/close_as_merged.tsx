import type { Ticket } from "@/api";
import PrLink from "@/shared/pr_link/pr_link";
import Timestamp from "@/shared/timestamp/timestamp";
import type { CloseAsMerged } from "./use_close_as_merged";
import "./close_as_merged.scss";

const CHECKS_LABEL: Record<string, string> = {
  passed: "passed",
  failed: "failed",
  not_run: "did not run",
};

function short(sha: string): string {
  return sha.slice(0, 12);
}

function prNumber(url: string): string {
  return url.split("/").filter(Boolean).pop() ?? url;
}

// CloseAsMergedPanel is the owner's notice that a ticket's change was merged
// outside slop-haus, with what the code host says about that merge, and the
// button that closes the ticket on it. It shows nothing for a ticket the host
// does not report merged into its base branch.
export default function CloseAsMergedPanel({ ticket, action }: { ticket: Ticket; action: CloseAsMerged }) {
  const { facts, close, confirmations } = action;
  if (!facts || (!facts.merged_into_base && !facts.ignored)) return null;
  const ignored = facts.ignored;
  return (
    <section className="ticket-detail__close-merged" aria-label="Merged outside slop-haus">
      {ignored && (
        <p role="status">
          <strong>
            PR #{prNumber(facts.pr_url)} was merged by {ignored.merger_login}, so the ticket wasn't closed.
          </strong>{" "}
          {ignored.reason} <Timestamp at={ignored.at} />
        </p>
      )}
      {facts.merged_into_base ? (
        <>
          <ul className="ticket-detail__close-merged-facts">
            <li>
              Merged head <code>{short(facts.merged_head ?? "")}</code>
              {facts.head_approved
                ? " is the approved head"
                : facts.approved_head
                  ? ` is not the approved head (${short(facts.approved_head)})`
                  : " was never approved"}
              .
            </li>
            <li>Checks on that head {CHECKS_LABEL[facts.checks ?? ""] ?? "did not run"}.</li>
            <li>
              {facts.merge_sha ? (
                <>
                  Merge commit <code>{short(facts.merge_sha)}</code>{" "}
                  {facts.merge_on_base ? "is" : "is not"} on {facts.base} now.
                </>
              ) : (
                <>The merge commit is unknown.</>
              )}
            </li>
          </ul>
          {confirmations ? (
            <div role="alertdialog" aria-label="Confirm close as merged" className="ticket-detail__close-merged-confirm">
              <p>
                <strong>Close it anyway?</strong> The code is on {facts.base} either way, but:
              </p>
              <ul>
                {confirmations.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
              <button type="button" className="ticket-detail__close-merged-button" onClick={action.handleConfirm} disabled={close.busy}>
                {close.busy ? "Closing…" : "Close as merged anyway"}
              </button>{" "}
              <button type="button" className="ticket-detail__close-merged-button" onClick={action.handleCancel} disabled={close.busy}>
                Cancel
              </button>
            </div>
          ) : (
            <button type="button" className="ticket-detail__close-merged-button" onClick={action.handleClose} disabled={close.busy}>
              {close.busy ? "Closing…" : "Close as merged"}
            </button>
          )}
          {close.error && (
            <p role="alert" className="ticket-detail__notice">
              {close.error}
            </p>
          )}
        </>
      ) : (
        <p>
          <PrLink url={ticket.pr_url} /> is not merged into {facts.base} on the code host, so it cannot be closed as
          merged.
        </p>
      )}
    </section>
  );
}
