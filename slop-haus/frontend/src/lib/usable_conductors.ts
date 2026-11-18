import type { Conductor, ConductorList } from "@/api";

// The remote controls a person may use: their own, switched on and not archived (the server
// refuses a disabled one every conversation route, readable or not).
// Never another person's, which a team's owner can see on the Remote Control tab to
// disable but not to use -- the server refuses it, and nothing here offers it.
// owned_by_me is checked as well as the list it came in, so a list that
// carried someone else's by mistake still would not offer it.
export function usableConductors(list: ConductorList): Conductor[] {
  return list.own.filter((c) => c.owned_by_me && c.disabled_at === null && c.archived_at === null);
}

// ownConductors is the remote controls a person manages in the sessions
// panel: their own, split into live (disabled ones too, to be enabled again,
// though the server refuses them every conversation route) and removed
// (archived). usableConductors is the subset that may hold sessions.
export function ownConductors(list: ConductorList): { live: Conductor[]; removed: Conductor[] } {
  const own = list.own.filter((c) => c.owned_by_me);
  return { live: own.filter((c) => c.archived_at === null), removed: own.filter((c) => c.archived_at !== null) };
}
