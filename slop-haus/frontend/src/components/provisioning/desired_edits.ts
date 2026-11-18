import type { DesiredAgent, DesiredAgentView } from "@/api";
import { DEFAULT_HARNESS, HARNESSES } from "@/domain/harnesses";

export type AgentKind = DesiredAgent["kind"];

export const KIND_LABELS: Record<AgentKind, string> = {
  slot: "Slot",
  steward: "Steward",
  remote_control: "Remote Control",
};

// A write names every agent of the document, so an edit starts from what
// was read and drops the supervisor's report.
// launch is left out unless the agent has one: a write that omits it keeps the stored value.
export function toWrite(agent: DesiredAgentView): DesiredAgent {
  return {
    id: agent.id,
    kind: agent.kind,
    name: agent.name,
    harness: agent.harness,
    state: agent.state,
    resume_generation: agent.resume_generation,
    ...(agent.launch ? { launch: agent.launch } : {}),
  };
}

export type Launch = "supervisor" | "manual";

export const LAUNCH_LABELS: Record<Launch, string> = {
  supervisor: "Run by the supervisor",
  manual: "Run by hand",
};

// Agents imported before the field existed read as "", and are always manual.
export const launchOf = (agent: DesiredAgentView): Launch => (agent.launch === "supervisor" ? "supervisor" : "manual");

export const setLaunch = (launch: Launch) => (a: DesiredAgent): DesiredAgent => ({ ...a, launch });

export const HARNESS_OPTIONS = HARNESSES.map((h) => ({ value: h.id as string, label: h.label }));

// pi runs every tool its model picks without asking, so the owner's container or VM is the only boundary.
export const PI_WARNING =
  "pi never asks before running a tool: whatever the model decides on, it runs. Run it on a machine that is a container or a VM, not your own.";

export function withAgentState(
  agents: DesiredAgentView[],
  id: string,
  change: (agent: DesiredAgent) => DesiredAgent,
): DesiredAgent[] {
  return agents.map((a) => (a.id === id ? change(toWrite(a)) : toWrite(a)));
}

// The supervisor applies it at the slot's next run, once the current one has ended.
export const setHarness = (harness: string) => (a: DesiredAgent): DesiredAgent => ({ ...a, harness });

export const pause = (a: DesiredAgent): DesiredAgent => ({ ...a, state: "paused" });
export const resume = (a: DesiredAgent): DesiredAgent => ({ ...a, state: "running" });
export const retire = (a: DesiredAgent): DesiredAgent => ({ ...a, state: "removed" });
// Raising the counter is what clears the supervisor's own pause; "running" alone never does.
export const clearSupervisorPause = (a: DesiredAgent): DesiredAgent => ({
  ...a,
  state: "running",
  resume_generation: a.resume_generation + 1,
});

export interface NewAgent {
  kind: AgentKind;
  name: string;
  harness?: string;
  launch?: Launch;
}

export function withNewAgent(agents: DesiredAgentView[], added: NewAgent): DesiredAgent[] {
  const { kind, name, harness, launch } = added;
  const extra: Partial<DesiredAgent> = {};
  if (kind === "remote_control") {
    extra.launch = launch ?? "supervisor";
    if (harness !== undefined && harness !== DEFAULT_HARNESS) extra.harness = harness;
  }
  return [...agents.map(toWrite), { kind, name, state: "running", resume_generation: 0, ...extra }];
}

export type AgentStatus =
  | { kind: "waiting" }
  | { kind: "provisioning" }
  | { kind: "failed"; reason: string }
  | { kind: "running" }
  | { kind: "workspace_out_of_date" }
  | { kind: "crash_loop"; reason: string }
  | { kind: "owner_hold" }
  | { kind: "supervisor_pause"; reason: string }
  | { kind: "retiring" }
  | { kind: "retired" };

// What the board shows for an agent. A report about an older generation says
// nothing about the current one, so until the supervisor has caught up the
// agent is "waiting for supervisor", whatever it reported before.
// The supervisor reports these two as the reason of a phase, since the report
// has no field of their own: a crash_loop pause starts "crash_loop:", and a live
// session on a stale workspace is a running phase with "workspace_out_of_date".
const CRASH_LOOP = "crash_loop";
const WORKSPACE_OUT_OF_DATE = "workspace_out_of_date";

export function agentStatus(agent: DesiredAgentView): AgentStatus {
  const { observed } = agent;
  if (agent.state === "removed") {
    if (observed.phase === "retired") return { kind: "retired" };
    // A refused removal (a slot with unpushed work) is reported as a failure: show why, not "Retiring" forever.
    if (observed.phase === "failed" && observed.generation >= (agent.generation ?? 0)) return { kind: "failed", reason: observed.reason ?? "" };
    return { kind: "retiring" };
  }
  const caughtUp = observed.generation >= (agent.generation ?? 0) && observed.phase !== undefined;
  if (!caughtUp) return { kind: "waiting" };
  switch (observed.phase) {
    case "failed":
      return { kind: "failed", reason: observed.reason ?? "" };
    case "provisioning":
      return { kind: "provisioning" };
    case "paused":
      if (agent.state === "paused") return { kind: "owner_hold" };
      if ((observed.reason ?? "").startsWith(CRASH_LOOP)) return { kind: "crash_loop", reason: observed.reason ?? "" };
      return { kind: "supervisor_pause", reason: observed.reason ?? "" };
    default:
      if (agent.state === "paused") return { kind: "owner_hold" };
      return (observed.reason ?? "").startsWith(WORKSPACE_OUT_OF_DATE) ? { kind: "workspace_out_of_date" } : { kind: "running" };
  }
}

export const STATUS_LABELS: Record<AgentStatus["kind"], string> = {
  waiting: "Waiting for supervisor",
  provisioning: "Provisioning",
  failed: "Failed",
  running: "Running",
  workspace_out_of_date: "Running; workspace out of date; will sync when idle",
  crash_loop: "Crash loop",
  owner_hold: "Paused by you",
  supervisor_pause: "Paused by the supervisor",
  retiring: "Retiring",
  retired: "Retired",
};

// What an owner runs for a Remote Control whose launch is manual; the supervisor
// starts the host itself for launch=supervisor.
export const REMOTE_CONTROL_COMMAND = ".slop-haus/run_remote_control.sh";
