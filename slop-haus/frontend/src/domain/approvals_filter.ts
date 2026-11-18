import type { ProposalTicketFilter } from "@/api";
import { TicketFilterModel, type FilterValues } from "./ticket_filter_model";

// The Approvals page's filter: it narrows the proposals to those about the
// tickets that match. Its address is ?q=, ?exclude=, ?priority=, ?created_from=,
// ?created_to=, ?updated_from= and ?updated_to=; defaults are left out.
//
// Parked, cancelled and done tickets are hidden by default: a proposal on one
// is rarely what a person came here to decide. Ticking them in the status
// filter shows them. The key is the tab's name, which is how a ticket's Back
// link finds this page's remembered filter.
export const APPROVALS_FILTER_MODEL = new TicketFilterModel("approvals", [
  { type: "text", id: "text", param: "q", label: "Title or text" },
  { type: "status", id: "status", param: "exclude", defaultExcluded: ["parked", "cancelled", "done"] },
  { type: "priority", id: "priority", param: "priority" },
  { type: "date_range", id: "created", fromParam: "created_from", toParam: "created_to", label: "Created", dateField: "created" },
  { type: "date_range", id: "updated", fromParam: "updated_from", toParam: "updated_to", label: "Updated", dateField: "updated" },
]);

export const DEFAULT_APPROVALS_VALUES: FilterValues = APPROVALS_FILTER_MODEL.defaults();

export interface ApprovalsSearch {
  q?: string;
  exclude?: string;
  priority?: string;
  created_from?: string;
  created_to?: string;
  updated_from?: string;
  updated_to?: string;
}

export function parseApprovalsSearch(raw: Record<string, unknown>): ApprovalsSearch {
  return APPROVALS_FILTER_MODEL.parseSearch(raw);
}

function one(value: string | readonly string[] | undefined): string | undefined {
  return (typeof value === "string" ? value : value?.[0]) || undefined;
}

// The list request for the filter, or null when every status is unticked and
// nothing can match (a request with no status would ask for them all).
export function approvalsTicketFilter(values: FilterValues): ProposalTicketFilter | null {
  const { status, params } = APPROVALS_FILTER_MODEL.query(values);
  if (status.length === 0) return null;
  const priority = params.priority;
  return {
    status,
    ...(one(params.q) && { q: one(params.q) }),
    ...(priority && { priority: typeof priority === "string" ? [priority] : [...priority] }),
    ...(one(params.created_from) && { created_from: one(params.created_from) }),
    ...(one(params.created_to) && { created_to: one(params.created_to) }),
    ...(one(params.updated_from) && { updated_from: one(params.updated_from) }),
    ...(one(params.updated_to) && { updated_to: one(params.updated_to) }),
  };
}
