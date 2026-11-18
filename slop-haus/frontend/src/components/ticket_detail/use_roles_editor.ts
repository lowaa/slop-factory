import { useEffect, useState } from "react";
import { apiClient, type AgentRole, type RoleMatch } from "@/api";
import { errorMessage } from "@/lib/use_async_action";
import { timestampsFrom, type TicketStore } from "./use_ticket_store";

export interface TenantRoles {
  roles: AgentRole[];
  loadFailed: boolean;
}

// The tenant's roles, for the role editor's picker -- fetched once, not per
// ticket, since it's the same roster for every ticket in the tenant.
// loadFailed is tracked apart from roles being empty, the same reason
// new_ticket_form.tsx's picker keeps the two apart: a fetch that failed
// must not read as "this tenant has no roles" to whoever opens the editor.
export function useTenantRoles(): TenantRoles {
  const [roles, setRoles] = useState<AgentRole[]>([]);
  const [loadFailed, setLoadFailed] = useState(false);
  useEffect(() => {
    let cancelled = false;
    apiClient
      .listAgentRoles()
      .then((loaded) => {
        if (!cancelled) setRoles(loaded);
      })
      .catch(() => {
        // The ticket itself still loads -- a failure here only means the
        // editor opens to an empty picker, with a hint saying why rather
        // than looking like the tenant just has no roles.
        if (!cancelled) setLoadFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);
  return { roles, loadFailed };
}

// RoleEditState is the role requirement editor while it is open -- roleIds
// and roleMatch are the pending choice, sent whole to setTicketRoles on
// save (its route replaces the requirement rather than patching it, so
// there's no base revision to carry the way the body editor's does).
//
// saving and error live in here rather than in a useAsyncAction because
// they belong to this one opening of the editor: closing it, or the ticket
// changing, drops them along with the choice.
interface RoleEditState {
  roleIds: string[];
  roleMatch: RoleMatch;
  saving: boolean;
  error: string | null;
}

export interface RolesEditorActions {
  roleEdit: RoleEditState | null;
  setRoleEdit: (roleEdit: RoleEditState | null) => void;
  startEditing: () => void;
  handleSave: () => void;
}

// useRolesEditor is the role requirement editor. Its state is held by
// TicketDetail, which calls this, for the reason useReviewActions gives.
export function useRolesEditor(store: TicketStore, ticketId: number): RolesEditorActions {
  const [roleEdit, setRoleEdit] = useState<RoleEditState | null>(null);

  function startEditing() {
    if (store.state.status !== "loaded") return;
    const { ticket } = store.state;
    setRoleEdit({
      roleIds: ticket.required_roles.map((r) => r.id),
      roleMatch: ticket.role_match,
      saving: false,
      error: null,
    });
  }

  // handleSave replaces the ticket's whole role requirement.
  // required_roles/role_match/no_eligible_agent take a turn on their own
  // generation counters, the same protection a PATCH gives every other
  // field: two saves in flight (this editor reopened and saved again
  // before the first reply lands) must settle on the later one, not
  // whichever response happens to arrive last.
  async function save() {
    if (store.state.status !== "loaded" || !roleEdit) return;
    const { roleIds, roleMatch } = roleEdit;
    const fields = ["required_roles", "role_match", "no_eligible_agent"] as const;
    const generations = fields.map((field) => store.beginFieldWrite(field));
    setRoleEdit({ ...roleEdit, saving: true, error: null });
    try {
      const updated = await apiClient.setTicketRoles(ticketId, roleIds, roleMatch);
      if (fields.every((field, i) => store.isCurrent(field, generations[i]))) {
        store.updateLoaded((prev) => ({
          ...prev,
          ticket: {
            ...prev.ticket,
            required_roles: updated.required_roles,
            role_match: updated.role_match,
            no_eligible_agent: updated.no_eligible_agent,
            ...timestampsFrom(prev.ticket, updated, false),
          },
        }));
      }
      setRoleEdit(null);
    } catch (err) {
      setRoleEdit({
        roleIds,
        roleMatch,
        saving: false,
        error: errorMessage(err, "Failed to update the roles this ticket needs."),
      });
    }
  }

  return { roleEdit, setRoleEdit, startEditing, handleSave: () => void save() };
}
