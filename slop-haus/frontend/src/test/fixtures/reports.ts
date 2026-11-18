import type {
  LlmUsageReport,
  LlmUsageSums,
  LockHistoryReport,
  Report,
  ReportState,
  ScopeCostReport,
  ShadowDecisionsReport,
} from "@/api";

const REPORT_STATES = ["preparing", "parked", "open", "in_progress", "in_merge_queue", "blocked", "discussion", "review"];

// A state with no stays, as the server sends it: no figures rather than zeros.
export function makeReportState(state: string, overrides: Partial<ReportState> = {}): ReportState {
  return { state, stays: 0, ongoing: 0, duration: null, share: null, ...overrides };
}

export function makeReport(overrides: Partial<Report> = {}): Report {
  return {
    from: "2026-09-03T12:00:00Z",
    to: "2026-10-03T12:00:00Z",
    history_from: "2026-01-01T00:00:00Z",
    lead: { done: 0, created_to_done: null, claimed_done: 0, claimed_to_done: null },
    lead_by_week: [],
    states: REPORT_STATES.map((s) => makeReportState(s)),
    throughput: [],
    now: [],
    parked_not_shown: 0,
    ...overrides,
  };
}

// Usage in one billing mode with nothing but what is given, as the server
// sends it.
export function makeUsageSums(overrides: Partial<LlmUsageSums> = {}): LlmUsageSums {
  return {
    billing_mode: "subscription",
    records: 1,
    requests: 0,
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    reasoning_tokens: 0,
    cost_micros: null,
    unpriced_records: 0,
    unpriced_requests: 0,
    ...overrides,
  };
}

export function makeLlmUsageReport(overrides: Partial<LlmUsageReport> = {}): LlmUsageReport {
  return {
    from: "2026-09-26T12:00:00Z",
    to: "2026-10-03T12:00:00Z",
    totals: [],
    unattributed: [],
    guidance: [],
    days: [],
    agents: [],
    models: [],
    tools: [],
    done_tickets: [],
    tickets: 0,
    unreliable_records: 0,
    unreliable_before: "2026-10-22T00:48:07Z",
    ...overrides,
  };
}

export function makeScopeCostReport(overrides: Partial<ScopeCostReport> = {}): ScopeCostReport {
  return {
    from: "2026-09-26T12:00:00Z",
    to: "2026-10-03T12:00:00Z",
    intervals: 0,
    waits: { tickets: 0, claimed: 0, ongoing: 0, total_seconds: 0, median_seconds: null },
    blocked_seconds: 0,
    idle_blocked_seconds: 0,
    tickets: [],
    blockers: [],
    paths: [],
    denials: { total: 0, granted: 0, narrowed: 0, gave_up: 0, open: 0, median_seconds_to_resolve: null, paths: [] },
    narrowings: 0,
    ...overrides,
  };
}

export function makeLockHistoryReport(overrides: Partial<LockHistoryReport> = {}): LockHistoryReport {
  return {
    from: "2026-09-26T12:00:00Z",
    to: "2026-10-03T12:00:00Z",
    total: { day: null, holds: 0, ongoing: 0, hold: null, queued: 0, wait: null },
    days: [],
    held_seconds: 0,
    overlap: [],
    ...overrides,
  };
}

export function makeShadowDecisionsReport(overrides: Partial<ShadowDecisionsReport> = {}): ShadowDecisionsReport {
  return {
    from: "2026-09-26T12:00:00Z",
    to: "2026-10-03T12:00:00Z",
    would_accept: 0,
    would_refuse: 0,
    accept_ci_passed: 0,
    accept_ci_failed: 0,
    accept_awaiting_ci: 0,
    reasons: [],
    failures: [],
    ...overrides,
  };
}
