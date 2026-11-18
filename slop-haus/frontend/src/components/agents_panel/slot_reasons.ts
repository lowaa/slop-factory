import type { SupervisorStatus } from "@/api";

// Every reason the server can send, in plain words. "other" and "unknown" say nothing, so the detail speaks.
export const SLOT_REASONS: Record<SupervisorStatus["slots"][number]["reason"], string> = {
  no_claimable: "waiting for work: no ticket can be claimed",
  fleet_paused: "the fleet is paused, so no new work starts",
  slot_paused: "this slot is paused",
  lock_queued: "queued behind another change for the integration lock",
  cooldown: "cooling down after handing a ticket back",
  backoff_after_error: "backing off after an error",
  waiting_ci: "waiting for the change's checks",
  reviewing: "running a review of its agent's change",
  updating: "updating its tools",
  starting: "starting up",
  other: "idle for another reason",
  unknown: "no reason reported",
};
