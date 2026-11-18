import { Button } from "@slop-factory/ui";
import type { VariantOption } from "@/api";
import { DEFAULT_VARIANT, MAX_RUNS, type ReplayBatchActions } from "./use_replay_batch";
import "./replay_batch_panel.scss";

interface ReplayBatchPanelProps {
  ticketCount: number;
  variants: VariantOption[];
  batch: ReplayBatchActions;
}

function plural(n: number, one: string): string {
  return `${n} ${one}${n === 1 ? "" : "s"}`;
}

// Replays of the picked done tickets under the chosen variants, added as one
// batch. The cost is shown before it starts -- the number of replays and the
// server's maximum -- and nothing is sent until that number is confirmed.
export default function ReplayBatchPanel({ ticketCount, variants, batch }: ReplayBatchPanelProps) {
  if (batch.unavailable) return null;
  const { plan, limits, submit, result } = batch;
  const count = plural(plan.count, "replay");
  return (
    <section className="replay-batch" aria-label="Replay batch">
      <h2>Replay {plural(ticketCount, "ticket")}</h2>
      <p className="replay-batch__warning" role="note">
        A replay runs a done ticket again as a shadow attempt, from the base commit the original started at and on the
        ticket as it was first given. It never merges and the ticket stays done, but its agent is a real agent: a ticket
        whose work deploys, pushes to main or touches live systems may do so unless the replay is stopped. Choosing the
        tickets is the safeguard; this applies to every ticket in the batch.
      </p>
      <fieldset disabled={submit.busy}>
        <legend>Variants</legend>
        <label>
          <input
            type="checkbox"
            checked={batch.variantIds.includes(DEFAULT_VARIANT)}
            onChange={() => batch.toggleVariant(DEFAULT_VARIANT)}
          />
          Default: agents in no variant
        </label>
        {variants.map((v) => (
          <label key={v.id}>
            <input type="checkbox" checked={batch.variantIds.includes(v.id)} onChange={() => batch.toggleVariant(v.id)} />
            {v.name} v{v.version}
          </label>
        ))}
        <label>
          Runs per variant
          <input
            type="number"
            min={1}
            max={MAX_RUNS}
            value={batch.runs}
            onChange={(e) => batch.setRuns(e.target.valueAsNumber)}
          />
        </label>
      </fieldset>
      <p className="replay-batch__count" role="status">
        This adds {count} ({plural(ticketCount, "ticket")} x {plural(batch.variantIds.length, "variant")} x{" "}
        {plural(batch.runs, "run")}).
        {limits && ` One batch may add at most ${limits.max_attempts}.`}
      </p>
      {plan.blocked && (
        <p role="alert" className="replay-batch__notice">
          {plan.blocked}
        </p>
      )}
      {submit.error && (
        <p role="alert" className="replay-batch__notice">
          {submit.error}
        </p>
      )}
      {batch.confirming && (
        <div className="replay-batch__confirm" role="group" aria-label="Confirm replays">
          <p>
            Start {count}? Each is a worker run that costs money. A ticket that already has its live attempts is refused,
            not queued.
          </p>
          <Button variant="primary" disabled={submit.busy} onClick={batch.confirm}>
            Confirm: add {count}
          </Button>
          <Button variant="ghost" onClick={batch.cancelConfirm}>
            Cancel
          </Button>
        </div>
      )}
      {result && (
        <div role="status" className="replay-batch__result">
          <p>
            Added {plural(result.added.reduce((n, a) => n + a.attempts.length, 0), "replay")} to{" "}
            {plural(result.added.length, "ticket")}.
          </p>
          {result.refused.length > 0 && (
            <>
              <p>Not added to {plural(result.refused.length, "ticket")}:</p>
              <ul>
                {result.refused.map((r) => (
                  <li key={r.ticket_id}>
                    #{r.ticket_id}: {r.reason}
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}
      <Button type="button" variant="primary" disabled={submit.busy || plan.blocked !== null} onClick={batch.review}>
        {submit.busy ? "Adding…" : `Review ${count}`}
      </Button>
    </section>
  );
}
