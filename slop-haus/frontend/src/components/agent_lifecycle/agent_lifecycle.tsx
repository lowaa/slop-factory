import { Button, Tooltip } from "@slop-factory/ui";
import { lifecycleStateOf, type LifecycleState, type LifecycleSubject } from "./lifecycle_state";
import type { AgentLifecycle } from "./use_agent_lifecycle";
import "./agent_lifecycle.scss";

const DISABLE_HINT = "Disable this agent: it can no longer claim tickets or sign in, and its holds are released.";
const ENABLE_HINT = "Enable this agent again so it can sign in and claim tickets.";
const REMOVE_HINT = "Remove this agent: it is disabled and hidden from the lists, and kept for history.";
const RESTORE_HINT = "Bring this agent back into the lists. It stays disabled until you enable it.";

export const REMOVE_COPY = "Hidden and kept for history (its tickets, comments and usage stay); you can restore it";

const LABELS: Record<LifecycleState, string> = { enabled: "Enabled", disabled: "Disabled", removed: "Removed" };

export function AgentStateBadge({ agent }: { agent: LifecycleSubject }) {
  const state = lifecycleStateOf(agent);
  return <span className={`agent-lifecycle__badge agent-lifecycle__badge--${state}`}>{LABELS[state]}</span>;
}

// The buttons; the confirm step and failures render in AgentLifecycleFeedback
// so a host can place them where there's room.
// removable is false for an agent this person may switch off but not remove
// or restore here: a remote control a supervisor runs, whose entry on that
// supervisor is what removes it, and which nothing would run again restored.
export function AgentLifecycleActions({ lifecycle, removable = true }: { lifecycle: AgentLifecycle<LifecycleSubject>; removable?: boolean }) {
  const { agent, status, toggleDisabled, askRemove, restore } = lifecycle;
  const state = lifecycleStateOf(agent);
  const busy = status === "working" || status === "removing";
  return (
    <>
      {state !== "removed" && (
        <Tooltip text={state === "disabled" ? ENABLE_HINT : DISABLE_HINT}>
          <Button size="sm" variant={state === "disabled" ? "secondary" : "danger"} onClick={toggleDisabled} disabled={busy}>
            {state === "disabled" ? "Enable" : "Disable"}
          </Button>
        </Tooltip>
      )}
      {!removable ? null : state === "removed" ? (
        <Tooltip text={RESTORE_HINT}>
          <Button size="sm" variant="secondary" onClick={restore} disabled={busy}>
            Restore
          </Button>
        </Tooltip>
      ) : (
        <Tooltip text={REMOVE_HINT}>
          <Button size="sm" variant="secondary" onClick={askRemove} disabled={busy || status === "confirming"}>
            Remove
          </Button>
        </Tooltip>
      )}
    </>
  );
}

export function AgentLifecycleFeedback({ lifecycle }: { lifecycle: AgentLifecycle<LifecycleSubject> }) {
  const { agent, status, error, confirmRemove, cancelRemove } = lifecycle;
  // A retry after a half-done Remove finds the agent already disabled.
  const retrying = error !== null && agent.disabled_at !== null;
  return (
    <>
      {(status === "confirming" || status === "removing") && agent.archived_at === null && (
        <div className="agent-lifecycle__confirm" role="group" aria-label={`Confirm removing ${agent.name}`}>
          <p>
            Remove <strong>{agent.name}</strong>? {REMOVE_COPY}.
          </p>
          <div className="agent-lifecycle__confirm-actions">
            <Button size="sm" variant="danger" onClick={confirmRemove} disabled={status === "removing"}>
              {status === "removing" ? "Removing…" : retrying ? "Retry remove" : "Remove"}
            </Button>
            <Button size="sm" variant="ghost" onClick={cancelRemove} disabled={status === "removing"}>
              Cancel
            </Button>
          </div>
        </div>
      )}
      {error && (
        <p className="agent-lifecycle__error" role="alert">
          {retrying && status === "confirming" ? `${agent.name} is disabled but not removed yet. ` : ""}
          {error}
        </p>
      )}
    </>
  );
}

export function ShowRemovedToggle({
  count,
  checked,
  onChange,
}: {
  count: number;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  if (count === 0) return null;
  return (
    <label className="agent-lifecycle__toggle">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      Show removed ({count})
    </label>
  );
}
