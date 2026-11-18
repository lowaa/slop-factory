import type { Status } from "@/api";
import { BOARD_STATUSES } from "./board_columns";
import { TicketQueryModel } from "@/lib/ticket_query/ticket_query";
import { SortModel, type SortId } from "@/lib/ticket_query/ticket_sort";
import { TicketFilterModel, type FilterValues } from "./ticket_filter_model";

// The columns that are always on the board; Done and Parked are the flags.
const BOARD_LIVE_STATUSES = BOARD_STATUSES.filter((s) => s !== "done" && s !== "parked");

// Its address is ?done=, ?parked= and ?q= (see routes.tsx); a default one is
// left out, so an unfiltered board is plain "/". Done and parked are flags
// rather than a status filter so that links already shared keep working.
export const BOARD_FILTER_MODEL = new TicketFilterModel("board", [
  { type: "text", id: "text", param: "q", label: "Search tickets", placeholder: "Search tickets, comments, handoffs", ticketJump: true },
  { type: "flag", id: "done", param: "done", label: "Include done", status: "done" },
  { type: "flag", id: "parked", param: "parked", label: "Include parked", status: "parked" },
], BOARD_LIVE_STATUSES);

// One sort applies to every column. Closed order is not offered: the board
// mixes statuses, most of which are not closed.
export const BOARD_SORT_MODEL = new SortModel(["newest", "oldest", "recently_updated", "priority"], "newest");

export const BOARD_QUERY_MODEL = new TicketQueryModel(BOARD_FILTER_MODEL, BOARD_SORT_MODEL);

// The board's address as the route types it: its filter, and the column a
// phone shows (?column=, see board.tsx).
export interface BoardSearch {
  column?: Status;
  q?: string;
  done?: true;
  parked?: true;
  sort?: SortId;
}

export function parseBoardSearch(raw: Record<string, unknown>): Omit<BoardSearch, "column"> {
  return BOARD_QUERY_MODEL.parseSearch(raw);
}

// The statuses the board has columns for under these filter values: the same
// list the columns are fetched from, so a column is drawn exactly when its
// status is asked for.
export function boardColumnStatuses(values: FilterValues): Status[] {
  return BOARD_FILTER_MODEL.query(values).status;
}
