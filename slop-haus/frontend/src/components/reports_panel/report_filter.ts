import type { Priority } from "@/api";

export const REPORT_DAYS = ["7", "30", "90"] as const;
export const REPORT_PRIORITIES: readonly Priority[] = ["low", "normal", "high", "urgent"];

export type ReportDays = 7 | 30 | 90;
export const DEFAULT_REPORT_DAYS: ReportDays = 30;

export interface ReportFilter {
  days: ReportDays;
  // Absent is every priority.
  priority?: Priority;
  // Parked tickets are left out unless asked for.
  includeParked?: boolean;
}

// The filter as it appears in the address: days a string, since that is
// what an address holds, and left out at its default.
export interface ReportFilterSearch {
  days?: (typeof REPORT_DAYS)[number];
  priority?: Priority;
  include_parked?: "1";
}

export function reportFilterFromSearch(search: ReportFilterSearch): ReportFilter {
  return {
    days: search.days ? (Number(search.days) as ReportDays) : DEFAULT_REPORT_DAYS,
    priority: search.priority,
    includeParked: search.include_parked === "1" || undefined,
  };
}

export function reportFilterSearch(filter: ReportFilter): ReportFilterSearch {
  return {
    days: filter.days === DEFAULT_REPORT_DAYS ? undefined : (String(filter.days) as ReportFilterSearch["days"]),
    priority: filter.priority,
    include_parked: filter.includeParked ? "1" : undefined,
  };
}
