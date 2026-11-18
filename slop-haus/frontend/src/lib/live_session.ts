import { isLiveState } from "@/domain/state_labels";
import type { Machine } from "./use_machines";

export interface LiveSession {
  conductorId: string;
  sessionId: string;
}

// The session the person is most likely after: the connected one. A machine
// has at most one live session, so across machines the most recently updated
// wins; the machine the address names, if it has one, beats the rest.
export function findLiveSession(machines: Machine[], preferConductorId?: string): LiveSession | undefined {
  // An unreadable list is not an empty one: the named machine may well have
  // a live session, so wait for it rather than pick another machine's.
  if (machines.some((m) => m.conductor.id === preferConductorId && m.sessions === null)) return undefined;
  const live = machines.flatMap((m) =>
    (m.sessions ?? []).filter((s) => isLiveState(s.state)).map((s) => ({ conductorId: m.conductor.id, session: s })),
  );
  const named = live.filter((l) => l.conductorId === preferConductorId);
  const pool = named.length > 0 ? named : live;
  let best: (typeof live)[number] | undefined;
  for (const l of pool) {
    if (!best || Date.parse(l.session.updated_at) > Date.parse(best.session.updated_at)) best = l;
  }
  return best && { conductorId: best.conductorId, sessionId: best.session.id };
}
