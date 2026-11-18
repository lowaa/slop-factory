import type { FilterSearch } from "@/domain/ticket_filter_model";
import { registerPersonalState } from "@/lib/personal_state";

// The address each filter instance last showed, by the instance's key, so a
// ticket's Back link (back_link.tsx) returns to the page as it was left
// rather than unfiltered. Module state, like the phone board's column
// (board_columns.ts): as long-lived as the page and no longer -- a reload
// has the address to go on. One entry per key, so instances never see each
// other's filter.
const remembered = new Map<string, FilterSearch>();

export function rememberFilterSearch(key: string, search: FilterSearch): void {
  remembered.set(key, search);
}

export function rememberedFilterSearch(key: string): FilterSearch | undefined {
  return remembered.get(key);
}

export function forgetRememberedFilters(): void {
  remembered.clear();
}

// Only view preferences, but still theirs -- and the search text may say
// what they were looking for.
registerPersonalState("remembered filters", { forget: forgetRememberedFilters });
