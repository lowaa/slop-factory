import type {
  BlockerCandidate,
  BlockerList,
  BlockingGraph,
  Comment,
  GraphEdge,
  GraphNode,
  Revision,
  ReviewFinding,
  ReviewRun,
  Status,
  StatusChange,
  Ticket,
  TicketAttempt,
  TicketPath,
  TicketRef,
} from "@/api";

export const EVERY_STATUS: Status[] = [
  "preparing",
  "parked",
  "open",
  "in_progress",
  "blocked",
  "discussion",
  "review",
  "done",
  "cancelled",
];

export function makePath(overrides: Partial<TicketPath> = {}): TicketPath {
  return { path: "server/internal/ticket/", source: "filed", ...overrides };
}

export function makeTicket(overrides: Partial<Ticket> = {}): Ticket {
  return {
    id: 1,
    title: "Fix the thing",
    body: "",
    priority: "normal",
    status: "open",
    paths: [],
    depends_on: [],
    blockers: [],
    created_by_agent_id: null,
    created_by_user_id: null,
    needs_discussion: false,
    resume_agent_id: null,
    affinity_until: null,
    requires_approval: false,
    approved_at: null,
    approved_by_user_id: null,
    changes_requested_at: null,
    post_reviewed_at: null,
    post_reviewed_by_user_id: null,
    revision: 0,
    pr_url: null,
    base_commit: null,
    base_ref: null,
    required_roles: [],
    role_match: "any",
    no_eligible_agent: false,
    external: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    closed_at: null,
    blocked_until: null,
    // A server with no rules, unless a test says otherwise: a fixture that
    // copied the real ones would be one more copy to keep in step.
    allowed_moves: EVERY_STATUS,
    can_ready: false,
    ...overrides,
  };
}

// A board card of one attempt of a ticket with several: the ticket's fields
// are the attempt's (its status), plus the attempt block.
export function makeAttemptCard(
  overrides: Partial<Ticket> = {},
  attempt: Partial<NonNullable<Ticket["attempt"]>> = {},
): Ticket {
  const status = overrides.status ?? "in_progress";
  return makeTicket({
    status,
    allowed_moves: [],
    attempt: {
      number: 1,
      total: 2,
      status: status as "in_progress" | "discussion" | "review",
      ticket_status: "in_progress",
      awaiting_choice: false,
      variant: { name: "baseline", version: 1, version_id: "v1", archived: false },
      worker_cost: [],
      ...attempt,
    },
    ...overrides,
  });
}

export function makeRevision(overrides: Partial<Revision> = {}): Revision {
  return {
    id: 1,
    revision: 1,
    editor_agent_id: null,
    editor_user_id: null,
    old_title: null,
    new_title: null,
    old_body: null,
    new_body: null,
    body_diff: null,
    created_at: "2026-01-02T00:00:00Z",
    ...overrides,
  };
}

export function makeStatusChange(overrides: Partial<StatusChange> = {}): StatusChange {
  return {
    id: 1,
    from_status: "open",
    to_status: "in_progress",
    changed_at: "2026-01-02T00:00:00Z",
    actor_kind: "agent",
    actor_id: "11111111-1111-1111-1111-111111111111",
    ...overrides,
  };
}

export function makeAttempt(overrides: Partial<TicketAttempt> = {}): TicketAttempt {
  return {
    number: 1,
    status: "queued",
    promoted: true,
    promoted_at: "2026-01-01T00:00:00Z",
    promoted_by_user_id: null,
    pr_url: null,
    approved_at: null,
    approved_by_user_id: null,
    changes_requested_at: null,
    review_started_at: null,
    started_revision: null,
    shadow: false,
    holder: null,
    variant: null,
    variant_settable: true,
    worker_cost: [],
    runs: [],
    signals: null,
    verdict: null,
    discussion: null,
    ...overrides,
  };
}

export function makeReviewFinding(overrides: Partial<ReviewFinding> = {}): ReviewFinding {
  return {
    id: "f1",
    file: "server/a.go",
    line: 12,
    severity: "major",
    summary: "A finding",
    detail: "Why it matters.",
    personas: ["standard"],
    label: null,
    ...overrides,
  };
}

export function makeReviewRun(overrides: Partial<ReviewRun> = {}): ReviewRun {
  return {
    id: "r1",
    posted_by_kind: "supervisor",
    posted_by_id: "sup1",
    posted_by_name: "home-1",
    kind: "review",
    status: "completed",
    failure: null,
    branch: "ticket-5-a1",
    head_sha: "abcdef1234567890",
    personas: ["standard"],
    verdict: "approve",
    summary: "Looks fine.",
    incomplete: false,
    provider: "claude",
    model: "claude-sonnet-5-5",
    started_at: "2026-01-02T00:00:00Z",
    finished_at: "2026-01-02T00:05:00Z",
    received_at: "2026-01-02T00:05:01Z",
    findings: [],
    ...overrides,
  };
}

export function makeComment(overrides: Partial<Comment> = {}): Comment {
  return {
    id: 1,
    attempt_number: null,
    author_agent_id: null,
    author_user_id: null,
    body: "Looks good",
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

export function makeGraphNode(overrides: Partial<GraphNode> = {}): GraphNode {
  return {
    id: 1,
    title: "Fix the thing",
    status: "open",
    priority: "normal",
    blocked_by: [],
    blocks: [],
    blocked: false,
    transitive_blockers: [],
    unblocks: 0,
    layer: 0,
    hidden: false,
    ...overrides,
  };
}

export function makeGraphEdge(overrides: Partial<GraphEdge> = {}): GraphEdge {
  return { blocker: 1, blocked: 2, ...overrides };
}

export function makeBlockingGraph(overrides: Partial<BlockingGraph> = {}): BlockingGraph {
  return { nodes: [], edges: [], layers: [], truncated: false, ...overrides };
}

export function makeBlockerCandidate(overrides: Partial<BlockerCandidate> = {}): BlockerCandidate {
  return { id: 1, title: "Some other ticket", status: "open", priority: "normal", ...overrides };
}

export function makeBlockerList(...blockers: TicketRef[]): BlockerList {
  return {
    blocked_by: blockers.map(({ id }) => id),
    blockers,
    status: blockers.length > 0 ? "blocked" : "open",
    allowed_moves: EVERY_STATUS,
    can_ready: false,
  };
}
