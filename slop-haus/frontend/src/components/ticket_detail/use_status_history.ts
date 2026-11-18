import { useEffect, useState } from "react";
import { apiClient, type StatusChange } from "@/api";

// The ticket's status changes, oldest first, refetched when the ticket's
// status moves (status is null until the ticket has loaded) so a move made
// on this page shows without a reload. Its own load, not the page's: the
// history is secondary, so a failure to read it must not replace the ticket
// with an error. The page is keyed by ticket, so one ticket's changes are
// never shown on another.
export function useStatusHistory(ticketId: number, status: string | null): StatusChange[] {
  const [changes, setChanges] = useState<StatusChange[]>([]);
  useEffect(() => {
    if (status === null) return;
    let cancelled = false;
    apiClient
      .listStatusHistory(ticketId)
      .then((loaded) => {
        if (!cancelled) setChanges(loaded);
      })
      .catch(() => {
        // Keeps what was shown; the next status move retries.
      });
    return () => {
      cancelled = true;
    };
  }, [ticketId, status]);
  return changes;
}
