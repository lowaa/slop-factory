import type { Priority, Ticket } from "@/api";
import type { SortId } from "../ticket_query/ticket_sort";

export type TicketComparator = (a: Ticket, b: Ticket) => number;

// Mirrors the server's sort of the same name (ticket/query.go), checked
// against Postgres's order of the same population by
// slop-haus/testdata/ticket_sort_orders.json: the id is the last key of
// every sort, so two tickets never tie.

// A timestamp to the precision the server keeps (microseconds). Date.parse
// stops at the millisecond, which would order two tickets a microsecond
// apart by id instead. Whole seconds as a number and the fraction as a
// zero-padded string compare exactly.
type Stamp = readonly [seconds: number, fraction: string];
const ZONED = /^(.*?)(?:\.(\d+))?(Z|[+-]\d\d:\d\d)$/;
// Never closed sorts last in both directions the closed sort is used in.
const NEVER: Stamp = [Number.NEGATIVE_INFINITY, ""];

function stamp(iso: string | null): Stamp {
  if (iso === null) return NEVER;
  const m = ZONED.exec(iso);
  if (!m) return [Date.parse(iso), ""];
  return [Date.parse(m[1] + m[3]) / 1000, (m[2] ?? "").padEnd(9, "0")];
}

function compareStamps(a: Stamp, b: Stamp): number {
  if (a[0] !== b[0]) return a[0] < b[0] ? -1 : 1;
  return a[1] < b[1] ? -1 : a[1] > b[1] ? 1 : 0;
}

const PRIORITY_RANK: Record<Priority, number> = { urgent: 0, high: 1, normal: 2, low: 3 };

// A Record over SortId, so registering a sort without saying where its
// tickets go does not compile.
export const COMPARATORS: Record<SortId, TicketComparator> = {
  newest: (a, b) => b.id - a.id,
  oldest: (a, b) => a.id - b.id,
  recently_updated: (a, b) => compareStamps(stamp(b.updated_at), stamp(a.updated_at)) || b.id - a.id,
  // closed_at desc with never-closed last, then id desc.
  recently_closed: (a, b) => compareStamps(stamp(b.closed_at), stamp(a.closed_at)) || b.id - a.id,
  // Urgent first, then newest.
  priority: (a, b) => PRIORITY_RANK[a.priority] - PRIORITY_RANK[b.priority] || b.id - a.id,
};

// The order of a list that may hold several cards of one ticket: the sort,
// then the attempt number, running the way the id does (the server's
// attemptKey), so a ticket's attempts stay together and in one order.
export function compareCards(sort: SortId): TicketComparator {
  const base = COMPARATORS[sort];
  const dir = sort === "oldest" ? 1 : -1;
  return (a, b) => base(a, b) || dir * ((a.attempt?.number ?? 0) - (b.attempt?.number ?? 0));
}

// Every update moves updated_at, unlike `revision`, which only counts title
// and body edits. An unparseable stamp is never "older", so it cannot make a
// copy be ignored.
// Compared to the microsecond, like the sorts: two updates in the same
// millisecond are still in an order.
export function isOlder(a: Ticket, than: Ticket): boolean {
  const [x, y] = [stamp(a.updated_at), stamp(than.updated_at)];
  if (Number.isNaN(x[0]) || Number.isNaN(y[0])) return false;
  return compareStamps(x, y) < 0;
}
