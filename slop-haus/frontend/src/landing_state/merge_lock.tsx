import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useRef,
  type ReactNode,
} from "react";
import { apiClient } from "@/api";
import { usePolledResource } from "@/lib/use_polled_resource";
import { ordinal } from "./describe_landing";

// Where a ticket stands for the merge lock in worker mode: the ticket stays
// in progress, so the lock is the only sign it is merging rather than coding.
export type MergeLockState =
  { kind: "holds" } | { kind: "waiting"; place: number };

const MergeLockContext = createContext<ReadonlyMap<number, MergeLockState>>(
  new Map(),
);

interface MergeLockProviderProps {
  // Changes with the board's live refresh, as for LandingQueueProvider.
  refreshToken: number;
  children: ReactNode;
}

// A failed read leaves the last answer (or none): the badge is an aside to the board.
export function MergeLockProvider({
  refreshToken,
  children,
}: MergeLockProviderProps) {
  const locks = usePolledResource(() => apiClient.listIntegrationLocks());
  const { reload } = locks;
  const first = useRef(true);
  useEffect(() => {
    if (first.current) first.current = false;
    else void reload();
  }, [refreshToken, reload]);
  const byTicket = useMemo(() => {
    const out = new Map<number, MergeLockState>();
    for (const lock of locks.data ?? []) {
      if (lock.holder) out.set(lock.holder.ticket_id, { kind: "holds" });
      // The queue is in the order the lock is handed out.
      lock.queue.forEach((w, i) =>
        out.set(w.ticket_id, { kind: "waiting", place: i + 1 }),
      );
    }
    return out;
  }, [locks.data]);
  return (
    <MergeLockContext.Provider value={byTicket}>
      {children}
    </MergeLockContext.Provider>
  );
}

export function useMergeLockState(ticketId: number): MergeLockState | null {
  return useContext(MergeLockContext).get(ticketId) ?? null;
}

export function describeMergeLock(state: MergeLockState): string {
  return state.kind === "holds"
    ? "holds the merge lock"
    : `waiting for the merge lock (${ordinal(state.place)})`;
}
