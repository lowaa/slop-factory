import type {
  Agent,
  AgentContext,
  AgentMessage,
  AgentRole,
  AgentSession,
  Lease,
} from "@/api";

export function makeAgentContext(overrides: Partial<AgentContext> = {}): AgentContext {
  return {
    id: 1,
    agent_id: "11111111-1111-1111-1111-111111111111",
    attempt_number: 1,
    body: "Handed off here for the human to weigh in.",
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

export function makeLease(overrides: Partial<Lease> = {}): Lease {
  return {
    ticket_id: 1,
    acquired_at: "2026-01-01T00:00:00Z",
    expires_at: "2026-01-01T00:10:00Z",
    ...overrides,
  };
}

export function makeAgentMessage(overrides: Partial<AgentMessage> = {}): AgentMessage {
  return {
    id: 1,
    agent_id: "11111111-1111-1111-1111-111111111111",
    kind: "message",
    state: "pending",
    author_user_id: "22222222-2222-2222-2222-222222222222",
    ticket_id: null,
    body: "stop, wrong approach",
    created_at: "2026-01-01T00:00:00Z",
    delivered_at: null,
    dropped_at: null,
    ...overrides,
  };
}

export function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "11111111-1111-1111-1111-111111111111",
    name: "agent-1",
    created_at: "2026-01-01T00:00:00Z",
    disabled_at: null,
    archived_at: null,
    replaces: null,
    replaced_by: null,
    kind: "worker",
    lease: null,
    session: null,
    roles: [],
    config_revision: 0,
    variant: null,
    harness_override_inactive: false,
    ...overrides,
  };
}

export function makeAgentRole(overrides: Partial<AgentRole> = {}): AgentRole {
  return {
    id: "99999999-9999-9999-9999-999999999999",
    name: "reviewer",
    description: "",
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

export function makeAgentSession(overrides: Partial<AgentSession> = {}): AgentSession {
  return {
    started_at: "2026-01-01T00:00:00Z",
    last_seen_at: "2026-01-01T00:05:00Z",
    harness: "",
    harness_version: "",
    provider: "",
    model: "",
    labels: {},
    config_revision: null,
    config_status: "none",
    bundle_hash: "",
    bundle_status: "unknown",
    stuck: null,
    ...overrides,
  };
}
