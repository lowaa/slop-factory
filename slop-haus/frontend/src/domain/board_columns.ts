import type { Status } from "@/api";
import { registerPersonalState } from "@/lib/personal_state";

// Stage 1 columns --
// `cancelled` is a terminal state deliberately left off the board (it's no
// longer live work, and unlike `done` there's no filter to bring it back),
// and there's no lease/claim UI yet since leases land in a later stage.
//
// Preparing sits first, before Open: it's a human still shaping the
// ticket, not yet in the queue, so the column order reads left to right as
// the ticket's own lifecycle -- shaped, then offered, then worked.
//
// Discussion sits after Blocked and before Done: like Blocked, it's a
// ticket that isn't claimable right now, just for a different reason
// (waiting on the human's answer rather than on another ticket).
//
// Review sits after Discussion and before Done, for the same reason: it
// isn't claimable either, just waiting on the human's approval rather than
// their answer.
//
// Parked sits first, before Preparing: unparking a ticket puts it in
// Preparing, so it is the step before it -- and at the edge of the board
// it reads as set aside, not as one more stage of live work.
//
// In merge queue sits after In progress: it is the same work, finished and
// waiting for the server to land it. It is the one conditional column (see
// ColumnContext): a tenant that lands by workers has none.
interface Column {
  status: Status;
  label: string;
  // Absent: always drawn (while its status is asked for).
  when?: (context: ColumnContext) => boolean;
}

// What decides the conditional columns. The In merge queue list is fetched
// whatever the mode, so anyQueued is known without a second request: tickets
// stay in the status after a switch back to worker mode until the runner has
// handed them back, and must not vanish from the board meanwhile.
export interface ColumnContext {
  mergeQueueMode: boolean;
  anyQueued: boolean;
}

export function columnContext(tickets: readonly { status: Status }[], mergeQueueMode: boolean): ColumnContext {
  return { mergeQueueMode, anyQueued: tickets.some((t) => t.status === "in_merge_queue") };
}

const COLUMNS: Column[] = [
  { status: "parked", label: "Parked" },
  { status: "preparing", label: "Preparing" },
  { status: "open", label: "Open" },
  { status: "in_progress", label: "In progress" },
  { status: "in_merge_queue", label: "In merge queue", when: (c) => c.mergeQueueMode || c.anyQueued },
  { status: "blocked", label: "Blocked" },
  { status: "discussion", label: "Discussion" },
  { status: "review", label: "Review" },
  { status: "done", label: "Done" },
];

// statusLabel is a status as the board names it -- a column's heading, or
// Cancelled, the one status with no column.
export function statusLabel(status: Status): string {
  if (status === "cancelled") return "Cancelled";
  return COLUMNS.find((c) => c.status === status)?.label ?? status;
}

// visibleColumns is the columns drawn for the statuses the board is asking
// the server for (the filter model's query, see board_filter.ts), so what
// the board draws and what it fetches are one list: Done and Parked have a
// column only while the filter asks for them.
export function visibleColumns(statuses: readonly Status[], context: ColumnContext) {
  return COLUMNS.filter((c) => statuses.includes(c.status) && (c.when?.(context) ?? true));
}

// Every status the board has a column for, which is what ?column= may name.
export const BOARD_STATUSES: readonly Status[] = COLUMNS.map((c) => c.status);

// The phone board shows one column at a time; this is where it opens when
// the URL does not say: In progress, the work happening now, or the first
// column after it with anything in it, wrapping round to Parked,
// Preparing and Open, which come before it. A board with nothing on it anywhere opens
// on In progress.
const FIRST_COLUMN: Status = "in_progress";

// shownColumn is the column the phone board shows: the requested one if
// the board has it (a stale ?column=done once Done is filtered off does
// not), else the default above.
export function shownColumn(tickets: readonly { status: Status }[], statuses: readonly Status[], requested?: Status, mergeQueueMode = false): Status {
  const columns = visibleColumns(statuses, columnContext(tickets, mergeQueueMode)).map((c) => c.status);
  if (requested && columns.includes(requested)) return requested;

  const start = columns.indexOf(FIRST_COLUMN);
  const rotated = [...columns.slice(start), ...columns.slice(0, start)];
  return rotated.find((status) => tickets.some((t) => t.status === status)) ?? FIRST_COLUMN;
}

// The column last chosen on the phone board, so the board's own Back link
// from a ticket (a plain link to "/", see back_link.tsx) returns to it
// rather than to the default. Module state, like pending_invite.ts: as
// long-lived as the page and no longer -- a reload has ?column= to go on.
let lastChosenColumn: Status | undefined;

export function rememberBoardColumn(status: Status): void {
  lastChosenColumn = status;
}

export function rememberedBoardColumn(): Status | undefined {
  return lastChosenColumn;
}

export function forgetBoardColumn(): void {
  lastChosenColumn = undefined;
}

registerPersonalState("board column", { forget: forgetBoardColumn });
