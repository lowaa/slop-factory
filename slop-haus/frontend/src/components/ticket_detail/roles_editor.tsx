import type { Ticket } from "@/api";
import { describeRequiredRoles, NO_ELIGIBLE_AGENT_WARNING } from "@/lib/roles";
import { RoleRequirementPicker } from "@/components/role_requirement_picker";
import type { RolesEditorActions, TenantRoles } from "./use_roles_editor";
import "./roles_editor.scss";

interface RolesEditorProps {
  ticket: Ticket;
  tenantRoles: TenantRoles;
  editor: RolesEditorActions;
}

export default function RolesEditor({ ticket, tenantRoles, editor }: RolesEditorProps) {
  const { roles, loadFailed } = tenantRoles;
  const { roleEdit, setRoleEdit } = editor;
  // Hidden outright when the tenant has no roles and the ticket requires
  // none: there's nothing to pick from and nothing to show, the same
  // reasoning new_ticket_form.tsx's picker follows for a fresh ticket.
  if (roles.length === 0 && ticket.required_roles.length === 0) return null;
  return (
    <section className="ticket-detail__roles">
      <h2>Roles</h2>
      {roleEdit ? (
        <div className="ticket-detail__role-editor">
          <RoleRequirementPicker
            roles={roles}
            selectedIds={roleEdit.roleIds}
            onSelectionChange={(ids) => setRoleEdit({ ...roleEdit, roleIds: ids })}
            matchMode={roleEdit.roleMatch}
            onMatchModeChange={(mode) => setRoleEdit({ ...roleEdit, roleMatch: mode })}
            disabled={roleEdit.saving}
            legend="Roles needed to claim this"
            emptyHint={
              loadFailed
                ? "Couldn't load your tenant's roles -- try again in a moment."
                : "No roles exist for this tenant yet."
            }
          />
          <div className="ticket-detail__role-editor-actions">
            <button type="button" onClick={editor.handleSave} disabled={roleEdit.saving}>
              {roleEdit.saving ? "Saving…" : "Save"}
            </button>
            <button type="button" onClick={() => setRoleEdit(null)} disabled={roleEdit.saving}>
              Cancel
            </button>
          </div>
          {roleEdit.error && (
            <p role="alert" className="ticket-detail__notice">
              {roleEdit.error}
            </p>
          )}
        </div>
      ) : (
        <div className="ticket-detail__role-display">
          <p>
            {ticket.required_roles.length > 0
              ? describeRequiredRoles(ticket.required_roles, ticket.role_match)
              : "Any agent may claim this."}
          </p>
          <button type="button" onClick={editor.startEditing}>
            Edit roles
          </button>
        </div>
      )}
      {ticket.no_eligible_agent && ticket.required_roles.length > 0 && (
        <p role="status" className="ticket-detail__role-warning">
          {NO_ELIGIBLE_AGENT_WARNING}
        </p>
      )}
    </section>
  );
}
