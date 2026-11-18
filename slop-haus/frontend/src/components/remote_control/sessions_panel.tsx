import { useState } from "react";
import { Button } from "@slop-factory/ui";
import type { Conductor } from "@/api";
import { AgentLifecycleActions, AgentLifecycleFeedback, AgentStateBadge, ShowRemovedToggle, useConductorLifecycle } from "@/components/agent_lifecycle";
import { AgentManagementNotes, AgentMenu } from "./agent_management";
import { useAgentManagement } from "./use_agent_management";
import Timestamp from "@/shared/timestamp/timestamp";
import { isLiveState, sessionLabel } from "@/domain/state_labels";
import { sessionName } from "./session_name";
import type { RemoteControlView } from "./remote_control_view";
import { updateAvailable, type Machine, type MachinesState } from "@/lib/use_machines";
import "./sessions_panel.scss";

interface SessionsPanelProps {
  state: MachinesState;
  open: boolean;
  onToggle: (open: boolean) => void;
  // The session on show, or none for a new one on newOn.
  sessionId: string | undefined;
  newOn: string | undefined;
  onOpenSession: (conductorId: string, sessionId: string) => void;
  onNewSession: (conductorId: string) => void;
  // Called once a remote control was disabled, enabled, removed, restored or
  // reconfigured, so the page reads the list again.
  onAgentChanged?: () => void;
  // On a phone the list is a screen of its own: always open, with no toggle.
  screen?: boolean;
  // Desktop only: the setup and running pages open in the main pane from here.
  // Not on a phone, where they sit under the list.
  view?: RemoteControlView;
  onOpenView?: (view: RemoteControlView) => void;
}

const noop = () => {};

const VIEW_LINKS: { id: RemoteControlView; label: string }[] = [
  { id: "setup", label: "Add a remote control" },
  { id: "running", label: "Running a remote control" },
  { id: "tenant", label: "Remote controls in this tenant" },
];

// SessionsPanel is the left panel: the person's sessions across their remote
// controls, grouped by machine, each machine marked online or offline. It
// collapses, and the page remembers whether it did for this device.
export default function SessionsPanel({
  state,
  open,
  onToggle,
  sessionId,
  newOn,
  onOpenSession,
  onNewSession,
  onAgentChanged = noop,
  screen = false,
  view,
  onOpenView,
}: SessionsPanelProps) {
  const shown = screen || open;
  return (
    <aside className={`sessions-panel${shown ? "" : " sessions-panel--closed"}`}>
      {!screen && (
        <Button className="sessions-panel__toggle" aria-expanded={open} aria-controls="sessions-panel-body" onClick={() => onToggle(!open)}>
          {open ? "Hide sessions" : "Show sessions"}
        </Button>
      )}
      {onOpenView && shown && (
        <nav className="sessions-panel__views" aria-label="Remote control help">
          {VIEW_LINKS.map(({ id, label }) => (
            <button
              key={id}
              type="button"
              className={`sessions-panel__view${view === id ? " sessions-panel__view--open" : ""}`}
              aria-current={view === id ? "page" : undefined}
              onClick={() => onOpenView(id)}
            >
              {label}
            </button>
          ))}
        </nav>
      )}
      <div id="sessions-panel-body" className="sessions-panel__body" hidden={!shown}>
        {state.status === "loading" && <p>Loading sessions…</p>}
        {state.status === "error" && (
          <p role="alert" className="sessions-panel__notice">
            {state.message}
          </p>
        )}
        {state.status === "loaded" && state.machines.length === 0 && state.removed.length === 0 && (
          <p className="sessions-panel__empty">You have no remote control yet. {onOpenView ? "Add one with the links above." : "Add one above."}</p>
        )}
        {state.status === "loaded" &&
          state.machines.map((machine) => (
            <MachineGroup
              key={machine.conductor.id}
              machine={machine}
              sessionId={sessionId}
              creating={newOn === machine.conductor.id}
              onOpenSession={onOpenSession}
              onNewSession={onNewSession}
              onAgentChanged={onAgentChanged}
            />
          ))}
        {state.status === "loaded" && <RemovedAgents removed={state.removed} onAgentChanged={onAgentChanged} />}
      </div>
    </aside>
  );
}

function MachineGroup({
  machine,
  sessionId,
  creating,
  onOpenSession,
  onNewSession,
  onAgentChanged,
}: {
  machine: Machine;
  sessionId: string | undefined;
  creating: boolean;
  onOpenSession: (conductorId: string, sessionId: string) => void;
  onNewSession: (conductorId: string) => void;
  onAgentChanged: () => void;
}) {
  const { conductor, online, build, sessions } = machine;
  const management = useAgentManagement(conductor, onAgentChanged);
  const disabled = conductor.disabled_at !== null;
  return (
    <section className="sessions-panel__machine" aria-label={conductor.name}>
      <h3 className="sessions-panel__machine-name">
        <span>{conductor.name}</span>
        {online !== null && (
          <span className={`sessions-panel__presence sessions-panel__presence--${online ? "online" : "offline"}`}>
            {online ? "Online" : "Offline"}
          </span>
        )}
        {disabled && <AgentStateBadge agent={conductor} />}
        <AgentMenu management={management} />
      </h3>
      {!disabled && build?.version && (
        <p className="sessions-panel__build">
          Host version {build.version}
          {updateAvailable(build) && (
            <span className="sessions-panel__update">
              {" "}
              &middot; Update available: restart the host to run {build.current}
            </span>
          )}
        </p>
      )}
      <AgentManagementNotes management={management} />
      {!disabled && <ul className="sessions-panel__list">
        <li>
          <button
            type="button"
            className={`sessions-panel__item${creating ? " sessions-panel__item--open" : ""}`}
            aria-current={creating ? "true" : undefined}
            onClick={() => onNewSession(conductor.id)}
          >
            + New session
          </button>
        </li>
        {sessions === null && (
          <li>
            <p className="sessions-panel__notice" role="status">
              Could not read this machine&apos;s sessions.
            </p>
          </li>
        )}
        {(sessions ?? []).map((s) => {
          const current = s.id === sessionId;
          const label = sessionLabel(s.state, online);
          const offline = online === false && isLiveState(s.state);
          const name = sessionName(s);
          return (
            <li key={s.id}>
              <button
                type="button"
                className={`sessions-panel__item${current ? " sessions-panel__item--open" : ""}`}
                aria-current={current ? "true" : undefined}
                title={name}
                onClick={() => onOpenSession(conductor.id, s.id)}
              >
                <span className="sessions-panel__item-line">
                  <span
                    className={`sessions-panel__state${offline ? " sessions-panel__state--offline" : ""}`}
                  >
                    {label}
                  </span>
                  <Timestamp at={s.updated_at} />
                </span>
                <span className="sessions-panel__name">{name}</span>
              </button>
            </li>
          );
        })}
      </ul>}
    </section>
  );
}

// RemovedAgents is the remote controls that were removed, kept for history,
// behind a toggle. Their sessions are not listed.
function RemovedAgents({ removed, onAgentChanged }: { removed: Conductor[]; onAgentChanged: () => void }) {
  const [shown, setShown] = useState(false);
  return (
    <div className="sessions-panel__removed">
      <ShowRemovedToggle count={removed.length} checked={shown} onChange={setShown} />
      {shown && (
        <ul className="sessions-panel__list" aria-label="Removed remote controls">
          {removed.map((c) => (
            <RemovedAgent key={c.id} conductor={c} onAgentChanged={onAgentChanged} />
          ))}
        </ul>
      )}
    </div>
  );
}

function RemovedAgent({ conductor, onAgentChanged }: { conductor: Conductor; onAgentChanged: () => void }) {
  const lifecycle = useConductorLifecycle(conductor, () => onAgentChanged());
  return (
    <li className="sessions-panel__removed-item">
      <span className="sessions-panel__removed-line">
        <span className="sessions-panel__removed-name">{conductor.name}</span>
        <AgentLifecycleActions lifecycle={lifecycle} removable={!conductor.run_by_supervisor_id} />
      </span>
      <AgentLifecycleFeedback lifecycle={lifecycle} />
    </li>
  );
}
