import { createContext, useContext, useEffect, useMemo, useRef, type ReactNode } from "react";
import { apiClient, type LandingEntry } from "@/api";
import { usePolledResource } from "@/lib/use_polled_resource";

const LandingQueueContext = createContext<ReadonlyMap<number, LandingEntry>>(new Map());

interface LandingQueueProviderProps {
  // Changes with the board's live refresh, so the queue is fetched on the
  // same stream rather than a second one.
  refreshToken: number;
  children: ReactNode;
}

// The landing queue by ticket, for the cards below it. A failed read leaves
// the last queue (or none): the badge is an aside to the board and must not
// replace it.
export function LandingQueueProvider({ refreshToken, children }: LandingQueueProviderProps) {
  // Reloaded in place rather than keyed on the token: a key change discards
  // the data, and every badge would blink off until the response arrived.
  const queue = usePolledResource(() => apiClient.listLandingQueue());
  const { reload } = queue;
  const first = useRef(true);
  useEffect(() => {
    if (first.current) first.current = false;
    else void reload();
  }, [refreshToken, reload]);
  const byTicket = useMemo(() => new Map((queue.data ?? []).map((e) => [e.ticket_id, e])), [queue.data]);
  return <LandingQueueContext.Provider value={byTicket}>{children}</LandingQueueContext.Provider>;
}

// null when the ticket is not landing or was not handed back, and outside a
// provider. The queue holds one attempt's change per ticket, so a card of
// another attempt (attemptNumber) shows nothing.
export function useLandingEntry(ticketId: number, attemptNumber?: number): LandingEntry | null {
  const entry = useContext(LandingQueueContext).get(ticketId) ?? null;
  if (entry && attemptNumber !== undefined && entry.attempt_number !== attemptNumber) return null;
  return entry;
}
