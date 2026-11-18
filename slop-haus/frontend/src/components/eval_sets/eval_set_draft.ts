import type { EvalSet, EvalSetDefinition, EvalSetFilter, Priority, Status } from "@/api";

export const FILTER_STATUSES: readonly Status[] = ["done", "cancelled", "review", "in_progress", "open", "parked"];
export const FILTER_PRIORITIES: readonly Priority[] = ["low", "normal", "high", "urgent"];

export type SetKind = "filter" | "pinned";

// What the editor holds: text as typed, turned into a definition on save.
export interface EvalSetDraft {
  name: string;
  description: string;
  kind: SetKind;
  statuses: string[];
  priorities: string[];
  q: string;
  closedFrom: string;
  closedTo: string;
  // Not editable here, but kept so that saving a set made through the API
  // does not widen what it matches.
  otherDates: Pick<EvalSetFilter, "created_from" | "created_to" | "updated_from" | "updated_to">;
  ticketIds: string;
}

export const EMPTY_DRAFT: EvalSetDraft = {
  name: "",
  description: "",
  kind: "filter",
  statuses: ["done"],
  priorities: [],
  q: "",
  closedFrom: "",
  closedTo: "",
  otherDates: {},
  ticketIds: "",
};

export function draftFromSet(set: EvalSet): EvalSetDraft {
  return {
    ...EMPTY_DRAFT,
    name: set.name,
    description: set.description,
    kind: set.filter ? "filter" : "pinned",
    statuses: set.filter?.statuses ?? [],
    priorities: set.filter?.priorities ?? [],
    q: set.filter?.q ?? "",
    closedFrom: set.filter?.closed_from ?? "",
    closedTo: set.filter?.closed_to ?? "",
    otherDates: {
      created_from: set.filter?.created_from,
      created_to: set.filter?.created_to,
      updated_from: set.filter?.updated_from,
      updated_to: set.filter?.updated_to,
    },
    ticketIds: (set.ticket_ids ?? []).map((id) => `#${id}`).join(" "),
  };
}

// The ticket numbers in text typed as "12, #13 14". Null when something in it
// is not a ticket number, so a typo is refused rather than dropped.
export function parseTicketIds(text: string): number[] | null {
  const parts = text.split(/[\s,]+/).filter(Boolean);
  const ids: number[] = [];
  for (const part of parts) {
    if (!/^#?[1-9]\d*$/.test(part)) return null;
    const id = Number(part.replace("#", ""));
    if (!ids.includes(id)) ids.push(id);
  }
  return ids;
}

export function filterFromDraft(draft: EvalSetDraft): EvalSetFilter {
  const filter: EvalSetFilter = {};
  for (const [key, value] of Object.entries(draft.otherDates)) if (value) filter[key as keyof typeof draft.otherDates] = value;
  if (draft.statuses.length) filter.statuses = draft.statuses;
  if (draft.priorities.length) filter.priorities = draft.priorities;
  if (draft.q.trim()) filter.q = draft.q.trim();
  if (draft.closedFrom) filter.closed_from = draft.closedFrom;
  if (draft.closedTo) filter.closed_to = draft.closedTo;
  return filter;
}

// definitionFromDraft is the request for a draft, or why it cannot be sent.
export function definitionFromDraft(draft: EvalSetDraft): EvalSetDefinition | string {
  const name = draft.name.trim();
  if (!name) return "Give the set a name.";
  const base = { name, description: draft.description.trim() };
  if (draft.kind === "filter") return { ...base, filter: filterFromDraft(draft) };
  const ids = parseTicketIds(draft.ticketIds);
  if (ids === null) return "Tickets are numbers, e.g. #12 #13.";
  return { ...base, ticket_ids: ids };
}

export function toggled(list: string[], value: string): string[] {
  return list.includes(value) ? list.filter((v) => v !== value) : [...list, value];
}

// A saved filter in words, for the list.
export function describeFilter(filter: EvalSetFilter): string {
  const parts: string[] = [];
  parts.push(filter.statuses?.length ? `status ${filter.statuses.join(", ")}` : "any status");
  if (filter.priorities?.length) parts.push(`priority ${filter.priorities.join(", ")}`);
  if (filter.q) parts.push(`matching “${filter.q}”`);
  for (const field of ["closed", "created", "updated"] as const) {
    const from = filter[`${field}_from`];
    const to = filter[`${field}_to`];
    if (from || to) parts.push(`${field} ${from ?? "…"} to ${to ?? "…"}`);
  }
  return parts.join("; ");
}
