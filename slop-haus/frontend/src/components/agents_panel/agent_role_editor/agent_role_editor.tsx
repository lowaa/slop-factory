import { useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type Agent, type AgentRole } from "@/api";
import "../agents_panel.scss";
import { errorMessage } from "@/lib/use_async_action";

// The checked set the person is editing -- "saving" keeps that same set on
// screen while the request is in flight rather than clearing the checkboxes
// out from under them.
type EditorState =
  | { status: "open"; checked: Set<string>; error: string | null }
  | { status: "saving"; checked: Set<string> };

interface AgentRoleEditorProps {
  agent: Agent;
  // Every role the tenant has, not just this agent's own (agent.roles).
  allRoles: AgentRole[];
  // setAgentRoles answers the whole updated agent; the panel swaps it into
  // its list rather than this editor deriving role objects from ids.
  onRolesChanged: (agent: Agent) => void;
  onClose: () => void;
}

export default function AgentRoleEditor({ agent, allRoles, onRolesChanged, onClose }: AgentRoleEditorProps) {
  const [state, setState] = useState<EditorState>(() => ({
    status: "open",
    checked: new Set(agent.roles.map((r) => r.id)),
    error: null,
  }));

  function toggleRole(roleId: string) {
    if (state.status !== "open") return;
    const checked = new Set(state.checked);
    if (checked.has(roleId)) checked.delete(roleId);
    else checked.add(roleId);
    setState({ status: "open", checked, error: null });
  }

  async function save() {
    const { checked } = state;
    setState({ status: "saving", checked });
    try {
      const updated = await apiClient.setAgentRoles(agent.id, Array.from(checked));
      onRolesChanged(updated);
      onClose();
    } catch (err) {
      setState({
        status: "open",
        checked,
        error: errorMessage(err, "Failed to update roles."),
      });
    }
  }

  const saving = state.status === "saving";
  return (
    <div className="agents-panel__role-editor" role="group" aria-label={`Roles for ${agent.name}`}>
      {allRoles.length === 0 ? (
        <>
          <p>No roles yet -- add one under Agent Roles below.</p>
          <Button size="sm" onClick={onClose}>
            Close
          </Button>
        </>
      ) : (
        <>
          <ul className="agents-panel__role-checklist">
            {allRoles.map((role) => (
              <li key={role.id}>
                <label>
                  <input
                    type="checkbox"
                    checked={state.checked.has(role.id)}
                    onChange={() => toggleRole(role.id)}
                    disabled={saving}
                  />
                  {role.name}
                </label>
              </li>
            ))}
          </ul>
          <div className="agents-panel__role-editor-actions">
            <Button size="sm" variant="primary" onClick={() => void save()} disabled={saving}>
              {saving ? "Saving…" : "Save"}
            </Button>
            <Button size="sm" variant="ghost" onClick={onClose} disabled={saving}>
              Cancel
            </Button>
          </div>
        </>
      )}
      {state.status === "open" && state.error && (
        <p role="alert" className="agents-panel__row-note agents-panel__row-note--error">
          {state.error}
        </p>
      )}
    </div>
  );
}
