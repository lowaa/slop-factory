import type {
  HarnessConfig,
  HarnessConfigScope,
  HarnessSettingDefinition,
  ServerSettingChange,
  Setting,
  TenantUserSetting,
  VariantVersion,
} from "@/api";

// harnessDefinitions mirrors the server's registry
// (server/internal/harnessconfig), patterns included.
const harnessDefinitions: HarnessSettingDefinition[] = [
  {
    harness: "claude", name: "model", kind: "model", options: [],
    pattern: "^[A-Za-z0-9][A-Za-z0-9._:/\\[\\]-]{0,99}$", label: "Main model", help: "The main session's model.",
  },
  {
    harness: "claude", name: "subagent_model", kind: "model", options: [],
    pattern: "^[A-Za-z0-9][A-Za-z0-9._:/\\[\\]-]{0,99}$", label: "Subagent model", help: "Every subagent's model.",
  },
  {
    harness: "claude", name: "effort", kind: "enum", options: ["low", "medium", "high", "xhigh", "max", "auto"],
    pattern: "", label: "Effort", help: "Reasoning effort.",
  },
  {
    harness: "pi", name: "provider", kind: "provider", options: [],
    pattern: "^[a-z0-9][a-z0-9._-]{0,31}$", label: "Provider", help: "pi's --provider.",
  },
];

export function makeHarnessConfig(overrides: Partial<HarnessConfig> = {}): HarnessConfig {
  return { definitions: harnessDefinitions, defaults: null, overrides: [], ...overrides };
}

export function makeHarnessConfigScope(overrides: Partial<HarnessConfigScope> = {}): HarnessConfigScope {
  return {
    agent_id: null,
    settings: {},
    revision: 1,
    updated_by: "55555555-5555-5555-5555-555555555555",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

export function makeVariantVersion(overrides: Partial<VariantVersion> = {}): VariantVersion {
  return {
    id: "aaaaaaaa-0000-0000-0000-000000000001",
    name: "fast",
    version: 1,
    harness: "claude",
    settings: { model: "haiku" },
    fleet_instructions_version_id: null,
    created_by: null,
    created_at: "2026-10-04T12:00:00Z",
    archived: false,
    archived_at: null,
    ...overrides,
  };
}

export function makeSetting(overrides: Partial<Setting> = {}): Setting {
  return {
    name: "ticket_processing_enabled",
    type: "bool",
    string_val: null,
    int_val: null,
    float_val: null,
    bool_val: true,
    is_default: true,
    updated_at: null,
    ...overrides,
  };
}

export function makeTenantUserSetting(overrides: Partial<TenantUserSetting> = {}): TenantUserSetting {
  return {
    name: "approvals.auto.status",
    type: "bool",
    str_val: null,
    int_val: null,
    float_val: null,
    bool_val: false,
    is_default: true,
    updated_at: null,
    ...overrides,
  };
}

export function makeServerSettingChange(overrides: Partial<ServerSettingChange> = {}): ServerSettingChange {
  const off = { type: "bool", bool_val: false, int_val: null, float_val: null, string_val: null } as const;
  return {
    id: 1,
    changed_at: "2026-10-11T08:00:00Z",
    changed_by_id: "55555555-5555-5555-5555-555555555555",
    changed_by_name: "Bea Example",
    changed_by_deleted: false,
    changed_by_former_member: false,
    before_history_kept: false,
    from: { ...off, bool_val: true },
    to: off,
    ...overrides,
  };
}
