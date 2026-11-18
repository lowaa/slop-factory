import type { TicketAttempt } from "@/api";

// A running attempt that started from the given ticket revision.
export function makeRunningAttempt(number: number, startedRevision: number | null, overrides: Partial<TicketAttempt> = {}): TicketAttempt {
  return {
    number,
    status: "in_progress",
    promoted: number === 1,
    promoted_at: null,
    promoted_by_user_id: null,
    pr_url: null,
    approved_at: null,
    approved_by_user_id: null,
    changes_requested_at: null,
    review_started_at: null,
    started_revision: startedRevision,
    shadow: false,
    holder: null,
    variant: null,
    variant_settable: false,
    worker_cost: [],
    runs: [],
    signals: null, verdict: null, discussion: null,
    ...overrides,
  };
}
