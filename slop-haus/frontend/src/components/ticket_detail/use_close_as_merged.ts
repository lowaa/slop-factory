import { useEffect, useState } from "react";
import { apiClient, type MergeFacts, type Ticket } from "@/api";
import { useAsyncAction, type AsyncAction } from "@/lib/use_async_action";
import type { TicketStore } from "./use_ticket_store";

export interface CloseAsMerged {
  // Null until the code host has answered, and for anyone it refuses: the
  // facts are the workspace owner's, so a member sees neither notice nor button.
  facts: MergeFacts | null;
  close: AsyncAction;
  // What the person must confirm before the close goes through; null when
  // they have not been asked.
  confirmations: string[] | null;
  handleClose: () => void;
  handleConfirm: () => void;
  handleCancel: () => void;
}

// A merged ticket is the only one with anything to read from the host, and a
// finished one is nothing to close.
function worthAsking(ticket: Ticket | null): boolean {
  return ticket !== null && !!ticket.pr_url && ticket.status !== "done" && ticket.status !== "cancelled";
}

// useCloseAsMerged is the owner's way to close a ticket whose change was
// merged outside slop-haus. Held by TicketDetail for the reason
// useReviewActions is.
export function useCloseAsMerged(store: TicketStore, ticketId: number): CloseAsMerged {
  const close = useAsyncAction();
  const [facts, setFacts] = useState<MergeFacts | null>(null);
  const [confirmations, setConfirmations] = useState<string[] | null>(null);
  const ticket = store.state.status === "loaded" ? store.state.ticket : null;
  const ask = worthAsking(ticket);
  const prUrl = ticket?.pr_url ?? null;

  useEffect(() => {
    setFacts(null);
    setConfirmations(null);
    if (!ask) return;
    let current = true;
    apiClient.getTicketMergeFacts(ticketId).then(
      (f) => {
        if (current) setFacts(f);
      },
      // A member's 403, or a host that cannot be reached: nothing to show.
      () => {},
    );
    return () => {
      current = false;
    };
  }, [ticketId, ask, prUrl]);

  function submit(confirm: boolean) {
    if (store.state.status !== "loaded") return;
    let generation = 0;
    void close.run(async () => {
      generation = store.beginFieldWrite("status");
      const result = await apiClient.closeTicketAsMerged(ticketId, confirm);
      if (result.ticket) {
        store.applyStatusWrite(result.ticket, generation);
        await store.refreshComments();
        setConfirmations(null);
      } else {
        setConfirmations(result.confirmations);
      }
    }, "Failed to close the ticket as merged.");
  }

  return {
    facts,
    close,
    confirmations,
    handleClose: () => submit(false),
    handleConfirm: () => submit(true),
    handleCancel: () => setConfirmations(null),
  };
}
