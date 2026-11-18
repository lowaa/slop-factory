import type { FilterSearch } from "@/domain/ticket_filter_model";

// Sorts are registered by name; the client never sends a column. The name is
// always sent, never left to the server's default (which depends on the
// filter), so the list knows the order it is showing and can place a live
// change in it. A new sort is one entry here, one comparator in
// paged_collection/ticket_order.ts and one in the server's registry
// (ticket/query.go).
export const SORTS = {
  newest: { label: "Newest first", api: "newest" },
  oldest: { label: "Oldest first", api: "oldest" },
  recently_updated: { label: "Recently updated", api: "recently_updated" },
  recently_closed: { label: "Recently closed or merged", api: "recently_closed" },
  priority: { label: "Priority (urgent first)", api: "priority" },
} as const;

export type SortId = keyof typeof SORTS;
export const SORT_IDS = Object.keys(SORTS) as SortId[];

export function isSortId(value: unknown): value is SortId {
  return typeof value === "string" && Object.hasOwn(SORTS, value);
}

// The sorts a page offers and the one it starts on. The sort is not part of
// the filter: it has its own address key, left out at the default, and a
// non-default sort does not make a list "filtered".
export class SortModel {
  static readonly PARAM = "sort";
  readonly offered: readonly SortId[];
  readonly defaultSort: SortId;

  constructor(offered: readonly SortId[], defaultSort: SortId) {
    if (!offered.includes(defaultSort)) throw new Error(`sort ${defaultSort} is the default but not offered`);
    this.offered = offered;
    this.defaultSort = defaultSort;
  }

  // Tolerant of anything an address can hold: a sort not offered here is the
  // default.
  fromSearch(raw: Record<string, unknown>): SortId {
    const v = raw[SortModel.PARAM];
    return isSortId(v) && this.offered.includes(v) ? v : this.defaultSort;
  }

  toSearch(sort: SortId): FilterSearch {
    return { [SortModel.PARAM]: sort === this.defaultSort ? undefined : sort };
  }
}
