import { apiClient } from "@/api";
import { parseStatusList } from "./status";
import type { FetchPage } from "@/lib/paged_collection/paged_collection";
import { TicketQueryModel } from "@/lib/ticket_query/ticket_query";
import { SORT_IDS, SortModel, type SortId } from "@/lib/ticket_query/ticket_sort";
import { TicketFilterModel, type FilterValues, type QueryParams } from "./ticket_filter_model";

// The Unreviewed page's filter. Its address is ?q=, ?from=, ?to= and ?exclude=;
// nothing about how far the list has been paged is in it, so a reload or a
// shared link starts at the top.
//
// Parked and cancelled are hidden by default, to match the other ticket
// lists. Done is not: this page lists only done tickets, so hiding them
// would leave it empty. The default is left out of the address.
export const UNREVIEWED_FILTER_MODEL = new TicketFilterModel("unreviewed", [
  { type: "text", id: "text", param: "q", label: "Title or text" },
  { type: "status", id: "status", param: "exclude", defaultExcluded: ["parked", "cancelled"] },
  { type: "date_range", id: "closed", fromParam: "from", toParam: "to", label: "Merged", dateField: "closed" },
]);

export const DEFAULT_UNREVIEWED_VALUES: FilterValues = UNREVIEWED_FILTER_MODEL.defaults();

// Most recently merged first unless the person picks another: the change
// most likely to still be built on is the one most worth catching a problem
// in. Every sort is offered, as every ticket here has been closed.
export const UNREVIEWED_SORT_MODEL = new SortModel(SORT_IDS, "recently_closed");

export const UNREVIEWED_QUERY_MODEL = new TicketQueryModel(UNREVIEWED_FILTER_MODEL, UNREVIEWED_SORT_MODEL);

export interface UnreviewedSearch {
  q?: string;
  exclude?: string;
  from?: string;
  to?: string;
  sort?: SortId;
}

export function parseUnreviewedSearch(raw: Record<string, unknown>): UnreviewedSearch {
  return UNREVIEWED_QUERY_MODEL.parseSearch(raw);
}

function single(value: QueryParams[string]): string | undefined {
  return typeof value === "string" ? value : value?.[0];
}

// The page request for the query's params, at the page size the query names.
export const fetchUnreviewedPage: FetchPage = async (params) => {
  const status = params.status;
  return apiClient.listTicketsPage({
    awaitingPostReview: true,
    statuses: parseStatusList(status),
    text: single(params.q) ?? "",
    closedFrom: single(params.closed_from),
    closedTo: single(params.closed_to),
    sort: single(params.sort),
    limit: Number(single(params.limit)) || undefined,
    cursor: single(params.cursor),
  });
};
