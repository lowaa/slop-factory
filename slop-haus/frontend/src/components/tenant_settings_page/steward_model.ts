import type { Agent, HarnessConfig } from "@/api";

// The steward runs on Claude Code, so only its claude settings apply.
const STEWARD_HARNESS = "claude";

export interface StewardModel {
  model: string | null;
  effort: string | null;
  // Where the model comes from: the steward agent's own config, or the tenant's defaults it falls back to.
  source: "own" | "default";
}

// Mirrors the server's layering: the steward's own override wins, setting by setting.
// The source is that of the model, which is what the owner is choosing.
export function stewardModel(config: HarnessConfig, agents: Agent[]): StewardModel {
  const steward = agents.find((a) => a.kind === "steward" && a.archived_at === null);
  const own = steward && config.overrides.find((o) => o.agent_id === steward.id)?.settings[STEWARD_HARNESS];
  const defaults = config.defaults?.settings[STEWARD_HARNESS];
  return {
    model: own?.model || defaults?.model || null,
    effort: own?.effort || defaults?.effort || null,
    source: own?.model ? "own" : "default",
  };
}

export function describeStewardModel(m: StewardModel): string {
  const source = m.source === "own" ? "steward's own" : "tenant default";
  const effort = m.effort ? `, effort ${m.effort}` : "";
  return m.model ? `Model: ${m.model}${effort} (${source})` : `Model: Claude Code's default${effort} (${source})`;
}
