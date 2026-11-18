import { Button, Dropdown } from "@slop-factory/ui";
import type { DesiredAgent, DesiredAgentView } from "@/api";
import Command from "@/shared/command/command";
import { DEFAULT_HARNESS } from "@/domain/harnesses";
import {
  HARNESS_OPTIONS,
  KIND_LABELS,
  LAUNCH_LABELS,
  PI_WARNING,
  REMOTE_CONTROL_COMMAND,
  STATUS_LABELS,
  agentStatus,
  clearSupervisorPause,
  launchOf,
  pause,
  resume,
  retire,
  setHarness as withHarness,
  setLaunch,
  withAgentState,
} from "./desired_edits";

interface Props {
  agent: DesiredAgentView;
  // The whole document the agent was read from: a write names every agent in it.
  document: DesiredAgentView[];
  busy: boolean;
  write: (agents: DesiredAgent[], failure: string) => Promise<unknown>;
}

// What a supervisor's desired-state entry adds to a worker: the state the
// supervisor reports, and the owner's ways to change it.
export default function WorkerControls({ agent: a, document, busy, write }: Props) {
  const status = agentStatus(a);
  const id = a.id ?? "";
  const edit = (change: (agent: DesiredAgent) => DesiredAgent, failure: string) => write(withAgentState(document, id, change), failure);
  return (
    <div className="provisioning__controls">
      <span className="provisioning__kind">{KIND_LABELS[a.kind]}</span>
      <span className={`provisioning__status provisioning__status--${status.kind}`}>
        {STATUS_LABELS[status.kind]}
        {/* Free text from the supervisor: a text node, never markup. */}
        {(status.kind === "failed" || status.kind === "supervisor_pause" || status.kind === "crash_loop") && status.reason !== "" && <>: {status.reason}</>}
      </span>
      {a.kind === "slot" && a.state !== "removed" && (
        <span className="provisioning__harness">
          <Dropdown<string>
            label={`Harness for ${a.name}`}
            value={a.harness ?? DEFAULT_HARNESS}
            options={HARNESS_OPTIONS}
            disabled={busy}
            onChange={(h) => edit(withHarness(h), "Could not change the harness.")}
          />
          {a.harness === "pi" && <p role="note">{PI_WARNING}</p>}
          {a.observed.harness && a.observed.harness !== (a.harness ?? DEFAULT_HARNESS) && (
            <span className="provisioning__observed_harness">running {a.observed.harness}</span>
          )}
        </span>
      )}
      <span className="provisioning__actions">
        {a.state === "running" && status.kind !== "retiring" && (
          <Button size="sm" disabled={busy} onClick={() => edit(pause, "Could not pause the agent.")}>
            Pause {a.name}
          </Button>
        )}
        {status.kind === "owner_hold" && (
          <Button size="sm" disabled={busy} onClick={() => edit(resume, "Could not resume the agent.")}>
            Resume {a.name}
          </Button>
        )}
        {(status.kind === "supervisor_pause" || status.kind === "crash_loop") && (
          <Button size="sm" disabled={busy} onClick={() => edit(clearSupervisorPause, "Could not resume the agent.")}>
            Clear pause on {a.name}
          </Button>
        )}
        {a.state !== "removed" && (
          <Button size="sm" variant="danger" disabled={busy} onClick={() => edit(retire, "Could not retire the agent.")}>
            Retire {a.name}
          </Button>
        )}
      </span>
      {a.kind === "remote_control" && a.state !== "removed" && (
        <div className="provisioning__command">
          <span className="provisioning__launch">{LAUNCH_LABELS[launchOf(a)]}</span>
          {launchOf(a) === "manual" ? (
            <>
              <span>Run this on the machine you want the session on:</span>
              <Command value={REMOTE_CONTROL_COMMAND} label={`the command for ${a.name}`} />
              <p>Stop the hand-launched host before handing it to the supervisor, which refuses to start a second one.</p>
              <Button size="sm" disabled={busy} onClick={() => edit(setLaunch("supervisor"), "Could not hand the agent to the supervisor.")}>
                Let the supervisor run {a.name}
              </Button>
            </>
          ) : (
            <Button size="sm" disabled={busy} onClick={() => edit(setLaunch("manual"), "Could not take the agent from the supervisor.")}>
              Run {a.name} by hand
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
