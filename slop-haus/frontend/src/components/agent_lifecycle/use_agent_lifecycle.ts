import { useState } from "react";
import { apiClient, type Agent } from "@/api";
import type { LifecycleSubject } from "./lifecycle_state";
import { errorMessage } from "@/lib/use_async_action";

export type LifecycleStatus = "idle" | "confirming" | "removing" | "working";

export interface AgentLifecycle<T extends LifecycleSubject = Agent> {
  agent: T;
  status: LifecycleStatus;
  // The last refusal or failure, kept until the next attempt.
  error: string | null;
  toggleDisabled: () => void;
  askRemove: () => void;
  cancelRemove: () => void;
  confirmRemove: () => void;
  restore: () => void;
}

// The three calls a kind of agent has. Workers and remote controls are both
// agents but have routes of their own, which only differ by path.
export interface LifecycleOps<T> {
  setDisabled: (id: string, disabled: boolean) => Promise<T>;
  archive: (id: string) => Promise<T>;
  unarchive: (id: string) => Promise<T>;
}

type Step<T> = (current: T) => Promise<T>;

const agentOps: LifecycleOps<Agent> = {
  setDisabled: (id, disabled) => apiClient.setAgentDisabled(id, disabled),
  archive: (id) => apiClient.archiveAgent(id),
  unarchive: (id) => apiClient.unarchiveAgent(id),
};

const conductorOps: LifecycleOps<LifecycleSubject> = {
  setDisabled: (id, disabled) => apiClient.setConductorDisabled(id, disabled),
  archive: (id) => apiClient.archiveConductor(id),
  unarchive: (id) => apiClient.unarchiveConductor(id),
};

// useAgentLifecycle drives disable/enable and remove/restore for one agent
// with the two calls the server has. Remove is "disable, then archive", so
// it can stop half way: every step reports the agent it got back, which
// reports the agent as it was left (partial), and a retry skips the disable
// that already happened. Intermediate results are not reported while the
// call is in flight: a host that filters disabled agents would unmount the
// row, and this hook's state with it.
export function useAgentLifecycle(agent: Agent, onChanged: (agent: Agent, partial: boolean) => void): AgentLifecycle {
  return useLifecycle(agent, onChanged, agentOps);
}

// A remote control a supervisor runs is removed by taking it off that
// supervisor, which retires it; the server refuses to archive it directly.
const supervisorRunOps = (supervisorId: string, name: string): LifecycleOps<LifecycleSubject> => ({
  ...conductorOps,
  archive: async (id) => {
    await apiClient.removeOwnRemoteControl(supervisorId, name);
    const { own } = await apiClient.listConductors({ includeArchived: true });
    const retired = own.find((c) => c.id === id);
    if (!retired) throw new Error(`${name} is gone from your remote controls`);
    return retired;
  },
});

// The same for a remote control, through the conductor routes. It reads only
// what LifecycleSubject has, so the tenant directory's rows, which are not
// whole conductors, can use it too. runBySupervisorId, for the owner's own
// rows, is the supervisor that runs it.
export function useConductorLifecycle(
  conductor: LifecycleSubject,
  onChanged: (conductor: LifecycleSubject, partial: boolean) => void,
  runBySupervisorId?: string,
): AgentLifecycle<LifecycleSubject> {
  const ops = runBySupervisorId ? supervisorRunOps(runBySupervisorId, conductor.name) : conductorOps;
  return useLifecycle(conductor, onChanged, ops);
}

function useLifecycle<T extends LifecycleSubject>(
  agent: T,
  onChanged: (agent: T, partial: boolean) => void,
  ops: LifecycleOps<T>,
): AgentLifecycle<T> {
  const [status, setStatus] = useState<LifecycleStatus>("idle");
  const [error, setError] = useState<string | null>(null);

  // A failed Remove stays on its confirm step so the retry is one click.
  async function run(steps: Step<T>[], busy: LifecycleStatus, failureStatus: LifecycleStatus) {
    setError(null);
    setStatus(busy);
    let current = agent;
    try {
      for (const step of steps) {
        current = await step(current);
      }
      onChanged(current, false);
      setStatus("idle");
    } catch (err) {
      if (current !== agent) onChanged(current, true);
      setError(errorMessage(err, "Failed to update agent."));
      setStatus(failureStatus);
    }
  }

  const disable: Step<T> = (a) => (a.disabled_at === null ? ops.setDisabled(a.id, true) : Promise.resolve(a));
  const archive: Step<T> = (a) => ops.archive(a.id);

  return {
    agent,
    status,
    error,
    toggleDisabled: () => void run([(a) => ops.setDisabled(a.id, a.disabled_at === null)], "working", "idle"),
    askRemove: () => {
      setError(null);
      setStatus("confirming");
    },
    cancelRemove: () => {
      setError(null);
      setStatus("idle");
    },
    confirmRemove: () => void run([disable, archive], "removing", "confirming"),
    restore: () => void run([(a) => ops.unarchive(a.id)], "working", "idle"),
  };
}
