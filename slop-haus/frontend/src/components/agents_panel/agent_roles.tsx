import { useState } from "react";
import { Button, Tooltip } from "@slop-factory/ui";
import { apiClient, RoleInUseError, type AgentRole, type RoleInUseTicket } from "@/api";
import "./agent_roles.scss";
import { errorMessage } from "@/lib/use_async_action";

export type RolesState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; roles: AgentRole[] };

interface AgentRolesProps {
  state: RolesState;
  // A role's name/description feeds AgentRow's chips, and deleting one
  // drops it from every agent that had it -- either way the roster the
  // panel already holds is stale, so the panel refetches both lists
  // rather than this component trying to patch them itself.
  onChanged: () => void;
}

// RowAction is at most one row's inline editor or delete confirm open at a
// time -- editing two roles at once has no real use here, and tracking a
// single active row keeps the state one value instead of a map.
// A delete's conflict is null until the server refuses with a
// RoleInUseError -- once it names the tickets, the plain confirm below
// turns into the clear-and-delete choice instead.
type RowAction =
  | { roleId: string; mode: "edit"; name: string; description: string; saving: boolean; error: string | null }
  | {
      roleId: string;
      mode: "delete";
      deleting: boolean;
      error: string | null;
      conflict: RoleInUseTicket[] | null;
    };

export default function AgentRoles({ state, onChanged }: AgentRolesProps) {
  const [rowAction, setRowAction] = useState<RowAction | null>(null);
  const [newName, setNewName] = useState("");
  const [newDescription, setNewDescription] = useState("");
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);

  async function handleCreate() {
    const name = newName.trim();
    if (name === "") {
      setCreateError("Give the role a name first.");
      return;
    }
    setCreating(true);
    setCreateError(null);
    try {
      await apiClient.createAgentRole(name, newDescription.trim());
      setNewName("");
      setNewDescription("");
      onChanged();
    } catch (err) {
      setCreateError(errorMessage(err, "Failed to create role."));
    } finally {
      setCreating(false);
    }
  }

  function startEdit(role: AgentRole) {
    setRowAction({ roleId: role.id, mode: "edit", name: role.name, description: role.description, saving: false, error: null });
  }

  async function handleSaveEdit() {
    if (!rowAction || rowAction.mode !== "edit") return;
    const { roleId, name, description } = rowAction;
    setRowAction({ ...rowAction, saving: true, error: null });
    try {
      await apiClient.updateAgentRole(roleId, { name: name.trim(), description: description.trim() });
      setRowAction(null);
      onChanged();
    } catch (err) {
      setRowAction({ ...rowAction, saving: false, error: errorMessage(err, "Failed to update role.") });
    }
  }

  function startDelete(role: AgentRole) {
    setRowAction({ roleId: role.id, mode: "delete", deleting: false, error: null, conflict: null });
  }

  // handleConfirmDelete is both the plain confirm's "Delete" and the
  // conflict choice's "Remove from N tickets and delete" -- the only
  // difference is clearTickets, which the first refuses to send at all
  // (deleteAgentRole's default) and the second sends explicitly, once the
  // human has seen exactly which tickets that opens up.
  async function handleConfirmDelete(clearTickets = false) {
    if (!rowAction || rowAction.mode !== "delete") return;
    setRowAction({ ...rowAction, deleting: true, error: null });
    try {
      await apiClient.deleteAgentRole(rowAction.roleId, clearTickets);
      setRowAction(null);
      onChanged();
    } catch (err) {
      if (err instanceof RoleInUseError) {
        setRowAction({ ...rowAction, deleting: false, error: null, conflict: err.tickets });
        return;
      }
      setRowAction({
        ...rowAction,
        deleting: false,
        error: errorMessage(err, "Failed to delete role."),
      });
    }
  }

  return (
    <section className="agents-panel__section agent-roles">
      <h2>Agent Roles</h2>

      {state.status === "loading" && <p>Loading roles…</p>}
      {state.status === "error" && (
        <p role="alert" className="agents-panel__notice">
          {state.message}
        </p>
      )}
      {state.status === "loaded" && (
        <>
          <ul className="agent-roles__list">
            {state.roles.map((role) => {
              const editing = rowAction?.roleId === role.id && rowAction.mode === "edit" ? rowAction : null;
              const deleting = rowAction?.roleId === role.id && rowAction.mode === "delete" ? rowAction : null;
              return (
                <li key={role.id} className="agent-roles__row">
                  {editing ? (
                    <div className="agent-roles__edit">
                      <label>
                        Name
                        <input
                          value={editing.name}
                          maxLength={64}
                          onChange={(e) => setRowAction({ ...editing, name: e.target.value })}
                          disabled={editing.saving}
                        />
                      </label>
                      <label>
                        Description
                        <input
                          value={editing.description}
                          maxLength={500}
                          onChange={(e) => setRowAction({ ...editing, description: e.target.value })}
                          disabled={editing.saving}
                        />
                      </label>
                      <div className="agent-roles__row-actions">
                        <Button size="sm" variant="primary" onClick={() => void handleSaveEdit()} disabled={editing.saving}>
                          {editing.saving ? "Saving…" : "Save"}
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => setRowAction(null)} disabled={editing.saving}>
                          Cancel
                        </Button>
                      </div>
                      {editing.error && (
                        <p role="alert" className="agents-panel__row-note agents-panel__row-note--error">
                          {editing.error}
                        </p>
                      )}
                    </div>
                  ) : (
                    <>
                      <span className="agent-roles__name">{role.name}</span>
                      <span className="agent-roles__description">{role.description}</span>
                      <span className="agent-roles__actions">
                        <Tooltip text="Change this role's name or description.">
                          <Button size="sm" onClick={() => startEdit(role)}>
                            Edit
                          </Button>
                        </Tooltip>
                        <Tooltip text="Delete this role. It is taken away from every agent that has it; you confirm first.">
                          <Button size="sm" variant="danger" onClick={() => startDelete(role)}>
                            Delete
                          </Button>
                        </Tooltip>
                      </span>
                    </>
                  )}
                  {deleting && (
                    <div className="agents-panel__confirm" role="group" aria-label={`Confirm delete role ${role.name}`}>
                      {deleting.conflict ? (
                        <>
                          <p>
                            Deleting it removes it from these {deleting.conflict.length} tickets as well. A
                            ticket whose only required role this is becomes claimable by any agent.
                          </p>
                          <ul className="agent-roles__conflict-tickets">
                            {deleting.conflict.map((t) => (
                              <li key={t.id}>
                                #{t.id} {t.title}
                              </li>
                            ))}
                          </ul>
                          <div className="agents-panel__confirm-actions">
                            <Button
                              size="sm"
                              variant="danger"
                              onClick={() => void handleConfirmDelete(true)}
                              disabled={deleting.deleting}
                            >
                              {deleting.deleting ? "Deleting…" : `Remove from ${deleting.conflict.length} tickets and delete`}
                            </Button>
                            <Button size="sm" variant="ghost" onClick={() => setRowAction(null)} disabled={deleting.deleting}>
                              Cancel
                            </Button>
                          </div>
                        </>
                      ) : (
                        <>
                          <p>
                            Delete role {role.name}? It is taken away from every agent that has it.
                          </p>
                          <div className="agents-panel__confirm-actions">
                            <Button
                              size="sm"
                              variant="danger"
                              onClick={() => void handleConfirmDelete()}
                              disabled={deleting.deleting}
                            >
                              {deleting.deleting ? "Deleting…" : "Delete"}
                            </Button>
                            <Button size="sm" variant="ghost" onClick={() => setRowAction(null)} disabled={deleting.deleting}>
                              Cancel
                            </Button>
                          </div>
                        </>
                      )}
                      {deleting.error && (
                        <p role="alert" className="agents-panel__row-note agents-panel__row-note--error">
                          {deleting.error}
                        </p>
                      )}
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
          {state.roles.length === 0 && <p className="agents-panel__empty">No roles yet.</p>}
        </>
      )}

      <div className="agent-roles__create">
        <label>
          Role name
          <input
            value={newName}
            maxLength={64}
            placeholder="reviewer"
            onChange={(e) => setNewName(e.target.value)} disabled={creating} />
        </label>
        <label>
          Description (optional)
          <input
            value={newDescription}
            maxLength={500}
            onChange={(e) => setNewDescription(e.target.value)} disabled={creating} />
        </label>
        <Button variant="primary" onClick={() => void handleCreate()} disabled={creating}>
          {creating ? "Adding…" : "Add role"}
        </Button>
      </div>
      {createError && (
        <p role="alert" className="agents-panel__notice">
          {createError}
        </p>
      )}
    </section>
  );
}
