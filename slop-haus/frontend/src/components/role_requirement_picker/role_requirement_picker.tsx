import { useId, type ReactNode } from "react";
import type { AgentRole, RoleMatch } from "@/api";
import "./role_requirement_picker.scss";

interface RoleRequirementPickerProps {
  // The tenant's whole roster to choose from -- not the caller's current
  // selection, which is selectedIds.
  roles: AgentRole[];
  selectedIds: string[];
  onSelectionChange: (ids: string[]) => void;
  // The any/all toggle only makes sense once there's an actual choice to
  // make (2+ roles picked), and only when a caller wants it at all --
  // leaving onMatchModeChange out hides it outright, for a picker with no
  // "any of"/"all of" reading (an agent's own role set, say).
  matchMode?: RoleMatch;
  onMatchModeChange?: (mode: RoleMatch) => void;
  disabled?: boolean;
  // The checkbox group's accessible name. A fieldset/legend gets this for
  // free from the browser, so callers see it as an ordinary labelled group
  // (getByRole("group", { name: legend })) without this component doing
  // anything special.
  legend: string;
  // Shown instead of the checklist when there are no roles to choose from
  // at all -- distinct from "none chosen", which is a perfectly ordinary
  // state (any agent may claim).
  emptyHint?: ReactNode;
}

// RoleRequirementPicker is a tenant's roles as a checkbox list, with an
// optional any/all toggle for what "the agent needs these" means once more
// than one is picked. Shared by new_ticket_form (filing with a
// requirement) and ticket_detail (editing one) rather than each rolling
// its own checklist -- see agents_panel/agent_row.tsx for the same
// checkbox-per-role shape without a match concept, which is why that
// toggle is opt-in rather than always rendered.
export default function RoleRequirementPicker({
  roles,
  selectedIds,
  onSelectionChange,
  matchMode,
  onMatchModeChange,
  disabled = false,
  legend,
  emptyHint,
}: RoleRequirementPickerProps) {
  const groupId = useId();

  if (roles.length === 0) {
    return emptyHint ? <p className="role-requirement-picker__empty">{emptyHint}</p> : null;
  }

  function toggleRole(roleId: string) {
    onSelectionChange(
      selectedIds.includes(roleId) ? selectedIds.filter((id) => id !== roleId) : [...selectedIds, roleId],
    );
  }

  const showMatchToggle = onMatchModeChange !== undefined && selectedIds.length >= 2;

  return (
    // A native <fieldset disabled> cascades to every control inside it,
    // including the nested match fieldset below -- one flag here instead
    // of threading `disabled` onto each checkbox and radio by hand.
    <fieldset className="role-requirement-picker" disabled={disabled}>
      <legend>{legend}</legend>
      <ul className="role-requirement-picker__list">
        {roles.map((role) => (
          <li key={role.id}>
            <label>
              <input type="checkbox" checked={selectedIds.includes(role.id)} onChange={() => toggleRole(role.id)} />
              {role.name}
            </label>
          </li>
        ))}
      </ul>
      {showMatchToggle && (
        <fieldset className="role-requirement-picker__match">
          <legend>Agent needs</legend>
          <label>
            <input
              type="radio"
              name={`${groupId}-match`}
              checked={matchMode !== "all"}
              onChange={() => onMatchModeChange("any")}
            />
            any of these
          </label>
          <label>
            <input
              type="radio"
              name={`${groupId}-match`}
              checked={matchMode === "all"}
              onChange={() => onMatchModeChange("all")}
            />
            all of these
          </label>
        </fieldset>
      )}
    </fieldset>
  );
}
