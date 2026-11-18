import { apiClient, type Status } from "@/api";
import { parseStatusList } from "@/domain/status";
import type { FetchPage } from "@/lib/paged_collection/paged_collection";
import { DEFAULT_PAGE_SIZE, TicketQueryModel, type TicketApiQuery, type TicketQuery } from "@/lib/ticket_query/ticket_query";
import type { FilterValues, QueryParams } from "@/domain/ticket_filter_model";
import { BOARD_STATUSES } from "@/domain/board_columns";
import type { SortId } from "@/lib/ticket_query/ticket_sort";
import { BOARD_FILTER_MODEL, BOARD_SORT_MODEL } from "@/domain/board_filter";

// The live columns hold the work in flight, which a board is read whole, so
// they ask for the largest page the server allows (its maximum is 200).
// Done and Parked are history and keep the default.
export const BOARD_LIVE_PAGE_SIZE = 200;

// The columns that list a ticket's attempts as cards of their own.
const ATTEMPT_CARD_COLUMNS: readonly Status[] = ["in_progress", "discussion", "review"];

// One column's query: the board's filter, narrowed to the column's status.
// Each column is its own list (its own cursor, its own "load more"), so the
// status it asks for is its own; whether the column is on the board at all
// is the filter's say, so a column the filter leaves off matches nothing and
// makes no request.
class BoardColumnQueryModel extends TicketQueryModel {
  private readonly column: Status;

  constructor(column: Status) {
    super(BOARD_FILTER_MODEL, BOARD_SORT_MODEL);
    this.column = column;
  }

  override initial(filter?: FilterValues, sort?: SortId): TicketQuery {
    const query = super.initial(filter, sort);
    const live = this.column !== "done" && this.column !== "parked";
    return { ...query, page: { limit: live ? BOARD_LIVE_PAGE_SIZE : DEFAULT_PAGE_SIZE } };
  }

  override toApi(query: TicketQuery): TicketApiQuery {
    const api = super.toApi(query);
    const status = api.status.includes(this.column) ? [this.column] : [];
    return { status, matchesNothing: status.length === 0, params: { ...api.params, status } };
  }
}

export const BOARD_COLUMN_QUERY_MODELS: Record<Status, BoardColumnQueryModel> = Object.fromEntries(
  BOARD_STATUSES.map((status) => [status, new BoardColumnQueryModel(status)]),
) as Record<Status, BoardColumnQueryModel>;

function single(value: QueryParams[string]): string | undefined {
  return typeof value === "string" ? value : value?.[0];
}

// The page request for a column's params, at the page size the query names.
// A work column's first page comes from the board, which lists it as attempt
// cards; its later pages are the tickets route with that page's cursor,
// which carries on in the same mode.
export const fetchBoardPage: FetchPage = async (params) => {
  const status = params.status;
  const statuses = parseStatusList(status);
  const filter = {
    statuses,
    text: single(params.q),
    sort: single(params.sort),
    limit: Number(single(params.limit)) || undefined,
    cursor: single(params.cursor),
  };
  const firstOfWorkColumn = !filter.cursor && statuses.length === 1 && ATTEMPT_CARD_COLUMNS.includes(statuses[0]);
  return firstOfWorkColumn ? apiClient.listBoardColumn(filter) : apiClient.listTicketsPage(filter);
};
