import type {
  GitHubAppWebhook,
  GitHubDelivery,
  IntakeDelivery,
  IntakeSource,
} from "@/api";

export function makeAppWebhook(overrides: Partial<GitHubAppWebhook> = {}): GitHubAppWebhook {
  return {
    state: "receiving",
    can_edit: true,
    expected_url: "https://hooks.example/hooks/github-app",
    configured_url: "https://hooks.example/hooks/github-app",
    applied_at: "2026-10-01T00:00:00Z",
    events: ["pull_request", "pull_request_review"],
    last_delivery: null,
    last_event: null,
    ...overrides,
  };
}

export function makeIntakeSource(overrides: Partial<IntakeSource> = {}): IntakeSource {
  return {
    id: "99999999-9999-9999-9999-999999999999",
    name: "alerts",
    adapter: "native",
    trust: "untrusted",
    auth: "bearer",
    default_status: "preparing",
    default_priority: "normal",
    max_priority: "high",
    reopen_policy: "new",
    resolve_closes: false,
    role_match: "any",
    roles: [],
    paths: [],
    url: "https://example.test/intake/99999999-9999-9999-9999-999999999999",
    created_by: "33333333-3333-3333-3333-333333333333",
    created_at: "2026-01-01T00:00:00Z",
    revoked_at: null,
    last_delivery_at: null,
    ...overrides,
  };
}

export function makeIntakeDelivery(overrides: Partial<IntakeDelivery> = {}): IntakeDelivery {
  return {
    id: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
    source_id: "99999999-9999-9999-9999-999999999999",
    event: "open",
    key: "disk-full",
    outcome: "applied",
    detail: "opened ticket #1",
    downgraded: false,
    ticket_id: null,
    received_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

export function makeGitHubDelivery(overrides: Partial<GitHubDelivery> = {}): GitHubDelivery {
  return {
    id: "88888888-8888-8888-8888-888888888888",
    installation_id: 2,
    event: "pull_request",
    action: "opened",
    outcome: "applied",
    detail: "moved ticket #1 to review",
    ticket_id: null,
    received_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}
