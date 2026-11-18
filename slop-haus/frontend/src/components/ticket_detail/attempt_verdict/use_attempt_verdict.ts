import { useState } from "react";
import { apiClient, type AttemptVerdictValue } from "@/api";
import { errorMessage, useAsyncAction, type AsyncAction } from "@/lib/use_async_action";
import type { TicketStore } from "../use_ticket_store";

export interface AttemptVerdictActions {
  note: string;
  setNote: (note: string) => void;
  submit: AsyncAction;
  // One click sets the verdict, with whatever note is typed.
  choose: (verdict: AttemptVerdictValue) => void;
}

// useAttemptVerdict sets the signed-in person's verdict on one attempt and
// puts the reply on the loaded ticket, so the tab shows it without a refetch.
// The tab is keyed by its attempt, so the note belongs to one attempt.
export function useAttemptVerdict(store: TicketStore, ticketId: number, attempt: number): AttemptVerdictActions {
  const [note, setNote] = useState("");
  const submit = useAsyncAction();

  function choose(verdict: AttemptVerdictValue) {
    void submit.run(async () => {
      const set = await apiClient.setAttemptVerdict(ticketId, attempt, verdict, note.trim());
      store.updateLoaded((prev) => ({
        ...prev,
        ticket: {
          ...prev.ticket,
          attempts: prev.ticket.attempts?.map((a) => (a.number === attempt ? { ...a, verdict: set } : a)),
        },
      }));
      setNote("");
    }, (err) => errorMessage(err, "Failed to save your verdict."));
  }

  return { note, setNote, submit, choose };
}
