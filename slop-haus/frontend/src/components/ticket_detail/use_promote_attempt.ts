import { useRef, useState } from "react";
import { apiClient, ApiError, type Ticket } from "@/api";
import { errorMessage, useAsyncAction, type AsyncAction } from "@/lib/use_async_action";
import { isFeatureDisabled } from "./feature_disabled";
import { timestampsFrom, type TicketStore } from "./use_ticket_store";

export interface PromoteAttemptActions {
  // The person pressed Promote and has yet to confirm.
  confirming: boolean;
  start: () => void;
  cancel: () => void;
  confirm: () => void;
  submit: AsyncAction;
  // The server does not serve promoting attempts (ATTEMPTS_PROMOTE_ENABLED is
  // off): learnt from its feature_disabled 404, as nothing else says it.
  unavailable: boolean;
}

// promoteOrConfirm posts the promotion. When the reply is lost (a gateway
// error or no response at all) the promotion may still have been made, so the
// ticket is read again before failing: an attempt now promoted is success.
async function promoteOrConfirm(ticketId: number, attempt: number): Promise<Ticket> {
  try {
    return await apiClient.promoteTicketAttempt(ticketId, attempt);
  } catch (err) {
    const replyLost = !(err instanceof ApiError) || err.status === 502 || err.status === 504;
    if (!replyLost) throw err;
    let current: Ticket;
    try {
      current = await apiClient.getTicket(ticketId);
    } catch {
      throw err;
    }
    if (current.attempts?.some((a) => a.number === attempt && a.promoted)) return current;
    throw err;
  }
}

// usePromoteAttempt is the "Promote attempt N" action of one attempt's tab. The
// tab is keyed by its attempt, so the confirmation never carries to another.
export function usePromoteAttempt(store: TicketStore, ticketId: number, attempt: number): PromoteAttemptActions {
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
          const generation = store.beginFieldWrite("status");
          const updated = await promoteOrConfirm(ticketId, attempt);
          if (!store.isCurrent("status", generation)) return;
          store.updateLoaded((prev) => ({
            ...prev,
            ticket: {
              ...prev.ticket,
              // Promoting an attempt awaiting choice approves it, which moves
              // the ticket's status with it.
              status: updated.status,
              no_eligible_agent: updated.no_eligible_agent,
              ...timestampsFrom(prev.ticket, updated, true),
            },
          }));
        },
        (err) => {
          if (isFeatureDisabled(err)) {
            setUnavailable(true);
            return null;
          }
          return errorMessage(err, "Failed to promote the attempt.");
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
