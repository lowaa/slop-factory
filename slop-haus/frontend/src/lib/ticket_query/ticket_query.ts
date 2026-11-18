import type { Status } from "@/api";
import { SORTS, SortModel, type SortId } from "./ticket_sort";
import type { FilterQuery, FilterSearch, FilterValues, QueryParams, TicketFilterModel } from "@/domain/ticket_filter_model";

export { SORTS, SortModel, type SortId } from "./ticket_sort";

export const DEFAULT_PAGE_SIZE = 50;

export interface TicketPageSpec {
  limit: number;
  // Opaque, from the previous page; absent for the first.
  cursor?: string;
}

// Filter + Sort + Page. The filter is the filter widget's value, so the
// address and the list read the same thing.
export interface TicketQuery {
  filter: FilterValues;
  sort: SortId;
  page: TicketPageSpec;
}

export interface TicketApiQuery {
  // Explicit status list; see FilterQuery.
  status: Status[];
  params: QueryParams;
  // Every status is unticked: nothing can match, so no request is needed.
  matchesNothing: boolean;
}

export class TicketQueryModel {
  readonly filterModel: TicketFilterModel;
  readonly sortModel: SortModel;

  constructor(filterModel: TicketFilterModel, sortModel: SortModel = new SortModel(["newest"], "newest")) {
    if (filterModel.fields.some((f) => (f.type === "date_range" ? [f.fromParam, f.toParam] : [f.param]).includes(SortModel.PARAM))) {
      throw new Error(`ticket query ${filterModel.key}: the address param ${SortModel.PARAM} belongs to the sort`);
    }
    this.filterModel = filterModel;
    this.sortModel = sortModel;
  }

  initial(filter: FilterValues = this.filterModel.defaults(), sort: SortId = this.sortModel.defaultSort): TicketQuery {
    return { filter, sort, page: { limit: DEFAULT_PAGE_SIZE } };
  }

  withFilter(query: TicketQuery, filter: FilterValues): TicketQuery {
    return { ...query, filter, page: { limit: query.page.limit } };
  }

  // Changing the sort starts the list over, like a filter change, and keeps
  // the filter.
  withSort(query: TicketQuery, sort: SortId): TicketQuery {
    return { ...query, sort, page: { limit: query.page.limit } };
  }

  // The filter and the sort are in the address, each under its own keys:
  // with "load more", a cursor would describe the last page and not what is
  // on screen.
  toSearch(query: TicketQuery): FilterSearch {
    return { ...this.filterModel.toSearch(query.filter), ...this.sortModel.toSearch(query.sort) };
  }

  sortFromSearch(raw: Record<string, unknown>): SortId {
    return this.sortModel.fromSearch(raw);
  }

  // The address as a route's validateSearch hands it on: the filter's keys
  // and the sort's, and nothing else of the page's.
  parseSearch<S extends object>(raw: Record<string, unknown>): S {
    return { ...this.filterModel.parseSearch(raw), ...this.sortModel.toSearch(this.sortModel.fromSearch(raw)) };
  }

  toApi(query: TicketQuery): TicketApiQuery {
    const { status, params }: FilterQuery = this.filterModel.query(query.filter);
    return {
      status,
      matchesNothing: status.length === 0,
      params: {
        ...params,
        status,
        sort: SORTS[query.sort].api,
        limit: String(query.page.limit),
        ...(query.page.cursor && { cursor: query.page.cursor }),
      },
    };
  }

  // Equal for queries that list the same tickets in the same order, however
  // far they have paged (the cursor is the position, not the query): a different key is a new list.
  key(query: TicketQuery): string {
    const { cursor: _cursor, ...params } = this.toApi(query).params;
    return JSON.stringify(Object.entries(params).sort(([a], [b]) => a.localeCompare(b)));
  }
}

// The query string, with list params repeated (?status=a&status=b). An
// empty status list would serialise to no status at all, which the server
// reads as every status, so it is refused: check matchesNothing first.
export function toUrlSearchParams(params: QueryParams): URLSearchParams {
  if (Array.isArray(params.status) && params.status.length === 0) {
    throw new Error("a query that matches nothing has no request to make");
  }
  const out = new URLSearchParams();
  for (const [name, value] of Object.entries(params)) {
    if (value === undefined) continue;
    for (const v of typeof value === "string" ? [value] : value) out.append(name, v);
  }
  return out;
}
