import { useRef, useState } from "react";
import { apiClient } from "@/api";
import { errorMessage, useAsyncAction, type AsyncAction } from "@/lib/use_async_action";
import { isFeatureDisabled } from "./feature_disabled";
import { timestampsFrom, type TicketStore } from "./use_ticket_store";

export interface CloseAttemptActions {
  // The person pressed Close and has yet to confirm.
  confirming: boolean;
  start: () => void;
  cancel: () => void;
  confirm: () => void;
  submit: AsyncAction;
  // The server does not serve closing attempts (ATTEMPTS_ADD_ENABLED is
  // off): learnt from its feature_disabled 404, as nothing else says it.
  unavailable: boolean;
}

// useCloseAttempt is the "Close attempt N" action of one attempt's tab. The
// tab is keyed by its attempt, so the confirmation never carries to another.
export function useCloseAttempt(store: TicketStore, ticketId: number, attempt: number): CloseAttemptActions {
  const [confirming, setConfirming] = useState(false);
  const [unavailable, setUnavailable] = useState(false);
  const submit = useAsyncAction();
  // State only reaches a second click after a render.
  const inFlight = useRef(false);

  function confirm() {
    if (store.state.status !== "loaded" || inFlight.current) return;
    inFlight.current = true;
    setConfirming(false);
    void submit
      .run(
        async () => {
          const generation = store.beginFieldWrite("attempts");
          const updated = await apiClient.closeTicketAttempt(ticketId, attempt);
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
          return errorMessage(err, "Failed to close the attempt.");
        },
      )
      .finally(() => {
        inFlight.current = false;
      });
  }

  return {
    confirming,
    start: () => setConfirming(true),
    cancel: () => setConfirming(false),
    confirm,
    submit,
    unavailable,
  };
}
