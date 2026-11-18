import { useRef, useState, type FormEvent } from "react";
import { apiClient, type TicketAttempt } from "@/api";
import { errorMessage, useAsyncAction, type AsyncAction } from "@/lib/use_async_action";
import { isFeatureDisabled } from "./feature_disabled";
import { timestampsFrom, type TicketStore } from "./use_ticket_store";

// The most the server takes in one batch.
export const MAX_ATTEMPT_ROWS = 5;

// The server's MaxLiveAttempts: the most attempts of one ticket live at once.
export const MAX_LIVE_ATTEMPTS = 5;

export interface AttemptCapacity {
  live: number;
  // How many more the server will take before it refuses for the cap.
  remaining: number;
  // What adding does to the promoted attempt, as the server decides it: the
  // new attempts are shadows once it has started, and an unstarted one loses
  // its promotion.
  promoted: { number: number; started: boolean } | null;
}

// Live is counted as the server counts it, so the form refuses nothing the
// server would take and offers nothing it would not.
export function attemptCapacity(attempts: TicketAttempt[]): AttemptCapacity {
  const live = attempts.filter((a) => a.status === "queued" || a.status === "in_progress" || a.status === "discussion")
    .length;
  const promoted = attempts.find((a) => a.promoted);
  return {
    live,
    remaining: Math.max(0, MAX_LIVE_ATTEMPTS - live),
    promoted: promoted ? { number: promoted.number, started: promoted.started_revision != null } : null,
  };
}

// A row's variant: a version id, or NONE for the agents in no variant.
export const NONE = "";

export interface AttemptRow {
  key: number;
  versionId: string;
}

export interface AddAttemptsActions {
  rows: AttemptRow[];
  capacity: AttemptCapacity;
  // The rows were submitted and wait for the person to confirm them.
  confirming: boolean;
  confirm: () => void;
  cancelConfirm: () => void;
  submit: AsyncAction;
  // The ticket is done, so the rows are replays of it (the server takes
  // nothing else for a done ticket).
  replay: boolean;
  // The server does not serve adding attempts (ATTEMPTS_ADD_ENABLED is off):
  // learnt from its feature_disabled 404 to the first try, as nothing else says it.
  unavailable: boolean;
  setVersion: (key: number, versionId: string) => void;
  addRow: () => void;
  removeRow: (key: number) => void;
  handleSubmit: (e: FormEvent<HTMLFormElement>) => void;
}

// useAddAttempts is the Add-attempt form: one row to start, a row more each
// time "Add another" is pressed, and every row sent in one request, which the
// server adds all of or none of. A refusal is the server's own words, shown
// beside the form with the rows kept, so nothing needs typing again.
export function useAddAttempts(store: TicketStore, ticketId: number): AddAttemptsActions {
  const nextKey = useRef(1);
  const [rows, setRows] = useState<AttemptRow[]>([{ key: 0, versionId: NONE }]);
  const [unavailable, setUnavailable] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const submit = useAsyncAction();
  // State only reaches a second click after a render; a ref stops two clicks
  // in the same instant (a ticket once got two attempts at once).
  const inFlight = useRef(false);
  const capacity = attemptCapacity(store.state.status === "loaded" ? (store.state.ticket.attempts ?? []) : []);
  const replay = store.state.status === "loaded" && store.state.ticket.status === "done";
  const maxRows = Math.min(MAX_ATTEMPT_ROWS, capacity.remaining);

  function reset() {
    setRows([{ key: nextKey.current++, versionId: NONE }]);
  }

  function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (store.state.status !== "loaded" || submit.busy || rows.length > capacity.remaining) return;
    setConfirming(true);
  }

  function confirm() {
    if (store.state.status !== "loaded" || inFlight.current) return;
    inFlight.current = true;
    setConfirming(false);
    void submit.run(
      async () => {
        const generation = store.beginFieldWrite("attempts");
        const versionIds = rows.map((r) => (r.versionId === NONE ? null : r.versionId));
        const updated = replay
          ? await apiClient.addTicketAttempts(ticketId, versionIds, true)
          : await apiClient.addTicketAttempts(ticketId, versionIds);
        reset();
        // A later write of the attempts (a variant choice) has the newer list.
        if (!store.isCurrent("attempts", generation)) return;
        store.updateLoaded((prev) => ({
          ...prev,
          ticket: {
            ...prev.ticket,
            attempts: updated.attempts,
            no_eligible_agent: updated.no_eligible_agent,
            ...timestampsFrom(prev.ticket, updated, false),
          },
        }));
      },
      (err) => {
        if (isFeatureDisabled(err)) {
          setUnavailable(true);
          return null;
        }
        return errorMessage(err, "Failed to add the attempts.");
      },
    ).finally(() => {
      inFlight.current = false;
    });
  }

  return {
    rows,
    capacity,
    confirming,
    replay,
    confirm,
    cancelConfirm: () => setConfirming(false),
    submit,
    unavailable,
    setVersion: (key, versionId) => {
      setConfirming(false);
      setRows((prev) => prev.map((r) => (r.key === key ? { ...r, versionId } : r)));
    },
    addRow: () => {
      setConfirming(false);
      setRows((prev) => (prev.length >= maxRows ? prev : [...prev, { key: nextKey.current++, versionId: NONE }]));
    },
    removeRow: (key) => {
      setConfirming(false);
      setRows((prev) => (prev.length > 1 ? prev.filter((r) => r.key !== key) : prev));
    },
    handleSubmit,
  };
}
