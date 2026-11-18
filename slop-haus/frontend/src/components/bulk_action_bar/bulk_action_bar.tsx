import { useState } from "react";
import { Button, Dropdown } from "@slop-factory/ui";
import { apiClient, BulkRefusedError, type BulkRefusal, type Priority, type Status, type Ticket } from "@/api";
import { statusLabel } from "@/domain/board_columns";
import { canReadyAll, commonMoves, noCommonMoveReason } from "./bulk_moves";
import "./bulk_action_bar.scss";
import { errorMessage } from "@/lib/use_async_action";
import { countBucket } from "@/usage/catalog";
import { useUsage } from "@/usage/use_usage";

// "ready" is not a status: it's the move to open, which lands each ticket on open or
// blocked by its own dependencies.
type MoveTarget = Status | "ready";

const PRIORITIES: Priority[] = ["low", "normal", "high", "urgent"];
const PRIORITY_OPTIONS = PRIORITIES.map((value) => ({ value, label: value }));

// Moves that take a ticket off the board for good ask first.
const CONFIRMED_MOVES: readonly Status[] = ["done", "cancelled"];

type Outcome =
  | { kind: "refused"; message: string; refusals: BulkRefusal[] }
  | { kind: "error"; message: string };

interface BulkActionBarProps {
  // The selected tickets, as the board has them now.
  selected: Ticket[];
  // The action went through: every selected ticket changed.
  onApplied: () => void;
  // The server refused the batch and changed nothing; the board is stale
  // about whichever tickets it refused.
  onRefused: () => void;
  onDeselect: (ids: number[]) => void;
  onExit: () => void;
}

export default function BulkActionBar({ selected, onApplied, onRefused, onDeselect, onExit }: BulkActionBarProps) {
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState<Status | null>(null);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const usage = useUsage();

  const ids = selected.map((t) => t.id);
  const count = selected.length;
  const moveOptions: { value: MoveTarget; label: string }[] = [
    ...(canReadyAll(selected) ? [{ value: "ready" as const, label: "Open" }] : []),
    ...commonMoves(selected).map((status) => ({ value: status, label: statusLabel(status) })),
  ];

  async function run(what: "move" | "mark_ready" | "priority", action: () => Promise<unknown>) {
    const count_bucket = countBucket(selected.length);
    const counted = (result: "done" | "refused" | "error") => usage.event("bulk_action", { action: what, outcome: result, count_bucket });
    setBusy(true);
    setConfirming(null);
    setOutcome(null);
    try {
      await action();
      counted("done");
      onApplied();
    } catch (err) {
      if (err instanceof BulkRefusedError) {
        counted("refused");
        setOutcome({ kind: "refused", message: err.message, refusals: err.refusals });
        onRefused();
      } else {
        counted("error");
        // The server's words, not a number of our own: it owns the cap on
        // how many tickets one action may name, and says so in its 422.
        setOutcome({ kind: "error", message: errorMessage(err, "The change failed.") });
      }
    } finally {
      setBusy(false);
    }
  }

  function move(to: MoveTarget) {
    if (to === "ready") void run("mark_ready", () => apiClient.bulkMarkTicketsReady(ids));
    else if (CONFIRMED_MOVES.includes(to)) setConfirming(to);
    else void run("move", () => apiClient.bulkSetTicketStatus(ids, to));
  }

  // Only the refused tickets still selected: deselecting by hand, or the
  // refresh dropping one that has gone, leaves fewer to offer.
  const stillRefused =
    outcome?.kind === "refused" ? outcome.refusals.map((r) => r.id).filter((id) => ids.includes(id)) : [];

  return (
    <section className="bulk-action-bar" aria-label="Bulk actions">
      <div className="bulk-action-bar__row">
        <p className="bulk-action-bar__count" role="status">
          {count} selected
        </p>
        {count > 0 &&
          (moveOptions.length > 0 ? (
            <Dropdown<MoveTarget>
              label="Move to"
              labelHidden
              placeholder="Move to…"
              value={null}
              options={moveOptions}
              onChange={move}
              disabled={busy}
            />
          ) : (
            <p className="bulk-action-bar__hint">{noCommonMoveReason(selected)}</p>
          ))}
        {count > 0 && (
          <Dropdown<Priority>
            label="Priority"
            labelHidden
            placeholder="Priority…"
            value={null}
            options={PRIORITY_OPTIONS}
            onChange={(priority) => void run("priority", () => apiClient.bulkSetTicketPriority(ids, priority))}
            disabled={busy}
          />
        )}
        {count === 0 && <p className="bulk-action-bar__hint">Pick tickets on the board to act on them.</p>}
        <Button className="bulk-action-bar__exit" onClick={onExit} disabled={busy}>
          Cancel
        </Button>
      </div>

      {confirming && (
        <div className="bulk-action-bar__confirm" role="group" aria-label="Confirm move">
          <p>
            Move {count} {count === 1 ? "ticket" : "tickets"} to {statusLabel(confirming)}?
          </p>
          <div className="bulk-action-bar__confirm-actions">
            <Button
              variant="danger"
              size="sm"
              onClick={() => void run("move", () => apiClient.bulkSetTicketStatus(ids, confirming))}
            >
              Move {count} to {statusLabel(confirming)}
            </Button>
            <Button size="sm" onClick={() => setConfirming(null)}>
              Don&apos;t move
            </Button>
          </div>
        </div>
      )}

      {outcome && (
        <div className="bulk-action-bar__outcome" role="alert">
          <p>{outcome.message}</p>
          {outcome.kind === "refused" && (
            <>
              <ul className="bulk-action-bar__refusals">
                {outcome.refusals.map((r) => (
                  <li key={r.id}>{r.message}</li>
                ))}
              </ul>
              {stillRefused.length > 0 && (
                <Button
                  size="sm"
                  onClick={() => {
                    onDeselect(stillRefused);
                    setOutcome(null);
                  }}
                >
                  Deselect the {stillRefused.length} that can&apos;t make this change
                </Button>
              )}
            </>
          )}
        </div>
      )}
    </section>
  );
}
