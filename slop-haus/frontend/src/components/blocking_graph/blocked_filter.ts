import { apiClient, type BlockedTicketsQuery } from "@/api";
import { parseStatusList } from "@/domain/status";
import type { FetchPage } from "@/lib/paged_collection/paged_collection";
import { TicketQueryModel } from "@/lib/ticket_query/ticket_query";
import { TicketFilterModel, type FilterValues, type QueryParams } from "@/domain/ticket_filter_model";

// The Blocked page's filter. Its address is ?q=, ?exclude=, ?priority=,
// ?updated_from= and ?updated_to=; defaults are left out. Nothing about how
// far the list has been paged is in it, so a reload starts at the top.
//
// Parked, cancelled and done tickets are hidden by default, as the page is
// about what is in the way now.
export const BLOCKED_FILTER_MODEL = new TicketFilterModel("blocked", [
  { type: "text", id: "text", param: "q", label: "Title or text" },
  { type: "status", id: "status", param: "exclude", defaultExcluded: ["parked", "cancelled", "done"] },
  { type: "priority", id: "priority", param: "priority" },
  { type: "date_range", id: "updated", fromParam: "updated_from", toParam: "updated_to", label: "Updated", dateField: "updated" },
]);

export const DEFAULT_BLOCKED_VALUES: FilterValues = BLOCKED_FILTER_MODEL.defaults();

// The order is the server's (what each ticket holds up, most first) and is
// not offered as a choice, so the query model has only its single default
// sort.
export const BLOCKED_QUERY_MODEL = new TicketQueryModel(BLOCKED_FILTER_MODEL);

export interface BlockedSearch {
  q?: string;
  exclude?: string;
  priority?: string;
  updated_from?: string;
  updated_to?: string;
}

export function parseBlockedSearch(raw: Record<string, unknown>): BlockedSearch {
  return BLOCKED_FILTER_MODEL.parseSearch(raw);
}

function list(value: QueryParams[string]): string[] | undefined {
  return value === undefined ? undefined : typeof value === "string" ? [value] : [...value];
}

function single(value: QueryParams[string]): string | undefined {
  return list(value)?.[0] || undefined;
}

export const fetchBlockedPage: FetchPage = async (params) => {
  const query: BlockedTicketsQuery = {
    status: params.status === undefined ? undefined : parseStatusList(params.status),
    priority: list(params.priority),
    q: single(params.q),
    updated_from: single(params.updated_from),
    updated_to: single(params.updated_to),
    limit: Number(single(params.limit)) || undefined,
    cursor: single(params.cursor),
  };
  return apiClient.listBlockedPage(query);
};
