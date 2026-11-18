import type {
  Conductor,
  ConductorDirectoryEntry,
  Conversation,
  ConversationDetail,
  ConversationEvent,
  Proposal,
} from "@/api";

export function makeConductor(overrides: Partial<Conductor> = {}): Conductor {
  return {
    id: "22222222-2222-2222-2222-222222222222",
    name: "conductor-1",
    created_at: "2026-01-01T00:00:00Z",
    disabled_at: null,
    archived_at: null,
    replaced_by: null,
    owner_user_id: "33333333-3333-3333-3333-333333333333",
    owned_by_me: true,
    ...overrides,
  };
}

export function makeConductorDirectoryEntry(overrides: Partial<ConductorDirectoryEntry> = {}): ConductorDirectoryEntry {
  return {
    id: "22222222-2222-2222-2222-222222222222",
    name: "conductor-1",
    owner_user_id: "33333333-3333-3333-3333-333333333333",
    owner_display_name: "Olive",
    disabled_at: null,
    archived_at: null,
    host_seen_at: "2026-01-01T00:00:00Z",
    has_live_session: false,
    ...overrides,
  };
}

export function makeProposal(overrides: Partial<Proposal> = {}): Proposal {
  return {
    id: "22222222-2222-2222-2222-222222222222",
    ticket_id: 7,
    ticket_title: "Pick a database",
    ticket_status: "discussion",
    subject: "ticket",
    approver_role: "member",
    agent_name: "console-1",
    kind: "discussion_answer",
    state: "pending",
    action_line: "Post the text below as your comment on #7. That answers its discussion and puts it back in the queue.",
    text: "Use Postgres.",
    outcome: "",
    stale_reason: "",
    created_at: "2026-01-01T00:00:00Z",
    expires_at: "2026-01-08T00:00:00Z",
    decided_at: null,
    decided_by_user_id: null,
    ...overrides,
  };
}

export function makeConversation(overrides: Partial<Conversation> = {}): Conversation {
  return {
    id: "44444444-4444-4444-8444-444444444444",
    conductor_id: "22222222-2222-2222-2222-222222222222",
    state: "ready",
    failure: null,
    first_line: null,
    title: null,
    last_seq: 0,
    truncated: false,
    context_tokens: 0,
    can_compact: false,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

export function makeConversationDetail(overrides: Partial<ConversationDetail> = {}): ConversationDetail {
  return {
    ...makeConversation(),
    host_online: true,
    host_capabilities: ["interrupt"],
    repo: null,
    scope_overlaps: [],
    ...overrides,
  };
}

export function makeConversationEvent(overrides: Partial<ConversationEvent> = {}): ConversationEvent {
  return { seq: 1, kind: "user", body: "hello", turn: null, block: null, created_at: "2026-01-01T00:00:00Z", ...overrides };
}

// makeEditProposal is a pending edit proposal: a new title, a one-line
// change in the description, a path swapped and a priority raised.
export function makeEditProposal(overrides: Partial<Proposal> = {}): Proposal {
  return makeProposal({
    id: "p-edit",
    ticket_id: 12,
    ticket_title: "Retire the old importer",
    ticket_status: "preparing",
    kind: "edit",
    text: "",
    action_line: "Apply the edit below to #12 (now preparing), as if you had made it on the board. Its status does not change.",
    edit: {
      title: { old: "Retire the importer", new: "Retire the old importer" },
      body: {
        old: "Remove the importer.\nKeep the exporter.",
        new: "Remove the importer.\nKeep the exporter and its tests.",
        diff: [
          { op: "equal", text: "Remove the importer." },
          { op: "delete", text: "Keep the exporter." },
          { op: "insert", text: "Keep the exporter and its tests." },
        ],
      },
      priority: { old: "normal", new: "high" },
      paths: ["importer/", "docs/"],
      paths_added: ["docs/"],
      paths_removed: ["legacy/"],
      overlaps: [{ ticket_id: 30, title: "Docs overhaul", status: "in_progress", path: "docs/", other: "docs/" }],
    },
    ...overrides,
  });
}

// makeManifestProposal is a pending workspace manifest proposal: one repo gains
// a deploy-only rule and a shared path, another is removed.
export function makeManifestProposal(overrides: Partial<Proposal> = {}): Proposal {
  return makeProposal({
    id: "p-manifest",
    ticket_id: null,
    ticket_title: "",
    subject: "tenant",
    approver_role: "owner",
    kind: "manifest_edit",
    text: "",
    action_line: "Replace the workspace manifest (revision 3 when proposed) with the one below, as your own edit.",
    manifest_edit: {
      base_revision: 3,
      repos: [
        {
          path: "app",
          remote: "github.com/acme/app",
          deploy_only: [{ path: "deploy/*/kustomization.yaml", lines: "\\s*newTag: \\S+" }],
          shared_paths: [{ path: "db/migrations/", resolve: "migration" }],
          protected_paths: [{ path: "infrastructure/", reason: "prod" }],
          independent_apps: ["slop-haus/"],
          batch_within_apps: true,
          skip_retest_across_apps: true,
        },
      ],
      changes: [
        {
          path: "app",
          op: "changed",
          deploy_only_added: [{ path: "deploy/*/kustomization.yaml", lines: "\\s*newTag: \\S+" }],
          shared_paths_added: [{ path: "db/migrations/", resolve: "migration" }],
          shared_paths_resolve_changed: [{ path: "gen/", old: "migration", new: "regenerate" }],
          protected_paths_added: [{ path: "infrastructure/", reason: "prod" }],
          protected_paths_removed: [{ path: "old/" }],
          independent_apps_added: ["slop-haus/"],
          independent_apps_removed: ["billing/"],
          batch_within_apps_changed: { old: false, new: true },
          skip_retest_across_apps_changed: { old: false, new: true },
        },
        { path: "lib", op: "removed", old_remote: "github.com/acme/lib" },
      ],
    },
    ...overrides,
  });
}
