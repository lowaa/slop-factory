import { useState, type FormEvent, type ReactNode } from "react";
import { Button, Dropdown } from "@slop-factory/ui";
import { ApiError, apiClient, type Agent, type DesiredAgent, type DesiredAgentView, type ProvisionSupervisor } from "@/api";
import { errorMessage, useAsyncAction } from "@/lib/use_async_action";
import { usePolledResource } from "@/lib/use_polled_resource";
import { useTenants } from "@/lib/shared_resources";
import { DEFAULT_HARNESS } from "@/domain/harnesses";
import StaleSupervisor from "./stale_supervisor";
import WorkerControls from "./worker_controls";
import SlotCapControl from "./slot_cap_control";
import UnmanagedSlots from "./unmanaged_slots";
import {
  HARNESS_OPTIONS,
  KIND_LABELS,
  LAUNCH_LABELS,
  PI_WARNING,
  agentStatus,
  withNewAgent,
  type AgentKind,
  type Launch,
} from "./desired_edits";

const KIND_OPTIONS = (Object.keys(KIND_LABELS) as AgentKind[]).map((k) => ({ value: k, label: KIND_LABELS[k] }));
const LAUNCH_OPTIONS = (Object.keys(LAUNCH_LABELS) as Launch[]).map((l) => ({ value: l, label: LAUNCH_LABELS[l] }));
const POLL_MS = 10_000;
const STALE_MESSAGE = "The agents changed since you looked, so the list was read again. Check it and retry.";

interface Props {
  supervisor: ProvisionSupervisor;
  // Whether the supervisor is polling; undefined until the status has loaded.
  online: boolean | undefined;
  // Set when the server speaks a newer supervisor protocol than this one.
  protocolNote: string | null;
  // Every worker the roster has on this machine, shown or not: a document
  // entry with no such worker gets a line of its own.
  rosterNames: ReadonlySet<string>;
  // The roster's rows for this machine. The caller draws them, handing each the
  // controls of its worker's entry in the supervisor's document (null if none).
  children: (controlsFor: (agent: Agent) => ReactNode) => ReactNode;
  onSupervisorChanged: () => Promise<void>;
}

export default function SupervisorGroup({ supervisor, online, protocolNote, rosterNames, children, onSupervisorChanged }: Props) {
  const doc = usePolledResource(() => apiClient.getDesiredState(supervisor.id), {
    intervalMs: POLL_MS,
    deps: [supervisor.id],
    fallbackError: "Could not load this supervisor's agents.",
  });
  const action = useAsyncAction();
  const [kind, setKind] = useState<AgentKind>("slot");
  const [name, setName] = useState("");
  const [launch, setLaunchChoice] = useState<Launch>("supervisor");
  const [harness, setHarness] = useState<string>(DEFAULT_HARNESS);
  const state = doc.data;
  const tenants = useTenants({ fallbackError: "Failed to load this tenant." });
  const isOwner = tenants.data?.find((t) => t.current)?.is_owner === true;

  // A write on a stale revision, or one the document's own rules refuse,
  // is a 409: read the document again so the next attempt starts from it.
  async function write(agents: DesiredAgent[], failure: string): Promise<boolean> {
    if (state === undefined) return false;
    let written = false;
    await action.run(async () => {
      try {
        await apiClient.putDesiredState(supervisor.id, state.revision, agents);
      } catch (err) {
        if (err instanceof ApiError && err.status === 409) await doc.reload();
        throw err;
      }
      await doc.reload();
      written = true;
    }, (err) => (err instanceof ApiError && err.status === 409 && /changed since/.test(err.message) ? STALE_MESSAGE : errorMessage(err, failure)));
    return written;
  }

  async function add(e: FormEvent) {
    e.preventDefault();
    if (state === undefined || name.trim() === "") return;
    // A refused write keeps what was typed so the retry is one click.
    if (await write(withNewAgent(state.agents, { kind, name: name.trim(), launch, harness }), "Could not add the agent.")) setName("");
  }

  const entries = state?.agents.filter((a) => agentStatus(a).kind !== "retired") ?? [];
  const desired = state?.agents ?? [];
  const controls = (a: DesiredAgentView) => <WorkerControls agent={a} document={desired} busy={action.busy} write={write} />;
  const byName = new Map(entries.map((a) => [a.name, a]));
  // Entries the supervisor has not made a worker of yet (or has, under a roster
  // row that is archived): without a line here they could not be retired.
  const pending = entries.filter((a) => !rosterNames.has(a.name));
  return (
    <section className="provisioning__group" aria-label={`Workers on ${supervisor.name}`}>
      <header className="provisioning__group-head">
        <h3>{supervisor.name}</h3>
        {online !== undefined && (
          <span className={`provisioning__online provisioning__online--${online ? "up" : "down"}`}>{online ? "online" : "offline"}</span>
        )}
        <SlotCapControl supervisor={supervisor} canEdit={isOwner} onChanged={onSupervisorChanged} />
      </header>
      {protocolNote !== null && <p className="provisioning__protocol" role="alert">{protocolNote}</p>}
      <StaleSupervisor supervisor={supervisor} canEnd={isOwner} onEnded={onSupervisorChanged} />
      {doc.error !== null && <p role="alert">{doc.error}</p>}
      {action.error !== null && <p role="alert">{action.error}</p>}
      {children((agent) => {
        const entry = byName.get(agent.name);
        return entry === undefined ? null : controls(entry);
      })}
      {pending.length > 0 && (
        <ul className="provisioning__agents" aria-label={`Waiting on ${supervisor.name}`}>
          {pending.map((a) => (
            <li key={a.id ?? a.name} className="provisioning__agent">
              <span className="provisioning__name">{a.name}</span>
              {controls(a)}
            </li>
          ))}
        </ul>
      )}
      {isOwner && <UnmanagedSlots supervisor={supervisor} revision={state?.revision} onImported={doc.reload} />}
      <form className="provisioning__add" onSubmit={add}>
        <Dropdown<AgentKind>
          label="Kind"
          value={kind}
          options={KIND_OPTIONS}
          onChange={setKind}
        />
        {kind === "remote_control" && (
          <>
            <Dropdown<Launch> label="Launch" value={launch} options={LAUNCH_OPTIONS} onChange={setLaunchChoice} />
            <Dropdown<string> label="Harness" value={harness} options={HARNESS_OPTIONS} onChange={setHarness} />
            {harness === "pi" && <p role="note">{PI_WARNING}</p>}
          </>
        )}
        <label>
          Name
          <input value={name} onChange={(e) => setName(e.target.value)} maxLength={100} />
        </label>
        <Button type="submit" disabled={action.busy || state === undefined || name.trim() === ""}>
          Add worker
        </Button>
      </form>
    </section>
  );
}
