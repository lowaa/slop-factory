import type { Agent } from "@/api";

// What the lifecycle reads of an agent, so workers and remote controls share it.
export type LifecycleSubject = Pick<Agent, "id" | "name" | "disabled_at" | "archived_at">;

export type LifecycleState = "enabled" | "disabled" | "removed";

export function lifecycleStateOf(agent: LifecycleSubject): LifecycleState {
  if (agent.archived_at !== null) return "removed";
  return agent.disabled_at !== null ? "disabled" : "enabled";
}
