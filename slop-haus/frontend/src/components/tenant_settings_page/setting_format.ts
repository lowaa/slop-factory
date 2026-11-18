import type { ServerSettingChange, Setting } from "@/api";

type SettingValue = ServerSettingChange["to"];

export function formatSettingValue(value: SettingValue | Setting): string {
  switch (value.type) {
    case "bool":
      return value.bool_val ? "on" : "off";
    case "int":
      return String(value.int_val);
    case "float":
      return String(value.float_val);
    case "string":
      return `"${value.string_val ?? ""}"`;
  }
}

// Who made a change is one of four cases the page keeps apart, so a removed
// account or a change from before history was kept is never passed off as a
// person.
export function changedByLabel(change: ServerSettingChange, selfId: string | undefined): string {
  if (change.before_history_kept) return "before history was kept";
  if (change.changed_by_deleted) return "a removed account";
  if (change.changed_by_id && change.changed_by_id === selfId) return "you";
  const name = change.changed_by_name ?? "a member";
  return change.changed_by_former_member ? `${name} (former member)` : name;
}
