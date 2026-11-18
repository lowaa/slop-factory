import { useEffect, useRef, useState } from "react";
import { apiClient } from "@/api";
import type { ReplayBatchLimits, ReplayBatchResult } from "@/api/tickets";
import { errorMessage, useAsyncAction, type AsyncAction } from "@/lib/use_async_action";
import { replaysUnavailable } from "./replays_unavailable";

// The default variant: the agents in no variant.
export const DEFAULT_VARIANT = "";

export const MAX_RUNS = 5;

// What the batch adds, as the server counts it.
export function attemptCount(tickets: number, variants: number, runs: number): number {
  return tickets * variants * runs;
}

export interface ReplayBatchPlan {
  count: number;
  perTicket: number;
  // Why the batch cannot start; null when it can.
  blocked: string | null;
}

export function planBatch(
  tickets: number,
  variants: number,
  runs: number,
  limits: ReplayBatchLimits | null,
): ReplayBatchPlan {
  const count = attemptCount(tickets, variants, runs);
  const perTicket = variants * runs;
  let blocked: string | null = null;
  if (limits === null) blocked = "The limits have not loaded.";
  else if (tickets === 0) blocked = "Pick at least one ticket.";
  else if (variants === 0) blocked = "Pick at least one variant.";
  else if (perTicket > limits.max_per_ticket)
    blocked = `${variants} variants x ${runs} runs is ${perTicket} replays per ticket; a ticket holds at most ${limits.max_per_ticket} live attempts.`;
  else if (count > limits.max_attempts)
    blocked = `${count} replays is over the maximum of ${limits.max_attempts} for one batch: pick fewer tickets, variants or runs.`;
  return { count, perTicket, blocked };
}

// How a batch is sent, for a source of tickets other than a picked list (an
// eval set runs through its own endpoint, which resolves its tickets itself).
export type ReplayBatchRunner = (
  versionIds: (string | null)[],
  runs: number,
  confirmAttempts: number,
) => Promise<ReplayBatchResult>;

export interface ReplayBatchActions {
  limits: ReplayBatchLimits | null;
  // The server does not serve replays.
  unavailable: boolean;
  variantIds: string[];
  runs: number;
  plan: ReplayBatchPlan;
  confirming: boolean;
  result: ReplayBatchResult | null;
  submit: AsyncAction;
  toggleVariant: (id: string) => void;
  setRuns: (runs: number) => void;
  review: () => void;
  cancelConfirm: () => void;
  confirm: () => void;
}

// useReplayBatch is the replay-batch panel: the default variant is selected
// alongside any challengers so the comparison is fair, the count and the
// server's maximum are shown before anything starts, and nothing is sent until
// the person has confirmed the number. Any change to the plan withdraws the
// confirmation, so the number confirmed is always the number sent.
// ticketIds are the tickets the batch will add to, and the count is computed
// from them; a run sends the batch with run instead of the default endpoint.
export function useReplayBatch(ticketIds: number[], run?: ReplayBatchRunner): ReplayBatchActions {
  const [limits, setLimits] = useState<ReplayBatchLimits | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [variantIds, setVariantIds] = useState<string[]>([DEFAULT_VARIANT]);
  const [runs, setRunsState] = useState(1);
  const [confirming, setConfirming] = useState(false);
  const [result, setResult] = useState<ReplayBatchResult | null>(null);
  const submit = useAsyncAction();
  const inFlight = useRef(false);

  useEffect(() => {
    let cancelled = false;
    apiClient
      .replayBatchLimits()
      .then((loaded) => {
        if (!cancelled) setLimits(loaded);
      })
      .catch((err) => {
        if (!cancelled && replaysUnavailable(err)) setUnavailable(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const tickets = new Set(ticketIds).size;
  const plan = planBatch(tickets, variantIds.length, runs, limits);

  function confirm() {
    if (plan.blocked !== null || inFlight.current) return;
    inFlight.current = true;
    setConfirming(false);
    const versionIds = variantIds.map((v) => (v === DEFAULT_VARIANT ? null : v));
    void submit
      .run(
        async () => {
          setResult(
            run
              ? await run(versionIds, runs, plan.count)
              : await apiClient.addReplayBatch([...new Set(ticketIds)], versionIds, runs, plan.count),
          );
        },
        (err) => {
          if (replaysUnavailable(err)) {
            setUnavailable(true);
            return null;
          }
          return errorMessage(err, "Failed to add the replays.");
        },
      )
      .finally(() => {
        inFlight.current = false;
      });
  }

  return {
    limits,
    unavailable,
    variantIds,
    runs,
    plan,
    confirming,
    result,
    submit,
    toggleVariant: (id) => {
      setConfirming(false);
      setVariantIds((prev) => (prev.includes(id) ? prev.filter((v) => v !== id) : [...prev, id]));
    },
    setRuns: (next) => {
      setConfirming(false);
      setRunsState(Math.min(MAX_RUNS, Math.max(1, Math.floor(next) || 1)));
    },
    review: () => {
      if (plan.blocked === null) setConfirming(true);
    },
    cancelConfirm: () => setConfirming(false),
    confirm,
  };
}
