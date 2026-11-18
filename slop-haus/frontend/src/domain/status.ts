import type { Status } from "@/api";

// Typed as Record so a status added to the schema fails the build here
// until it is listed.
const STATUS_SET: Record<Status, true> = {
  preparing: true,
  parked: true,
  open: true,
  in_progress: true,
  in_merge_queue: true,
  blocked: true,
  discussion: true,
  review: true,
  done: true,
  cancelled: true,
};

export function parseStatus(s: string | null | undefined): Status | undefined {
  return typeof s === "string" && Object.hasOwn(STATUS_SET, s) ? (s as Status) : undefined;
}

// The recognised statuses of a URL/search value (one string or several),
// dropping the rest: a status since renamed must not reach the server.
export function parseStatusList(value: string | readonly string[] | null | undefined): Status[] {
  const items = value == null ? [] : typeof value === "string" ? [value] : value;
  return items.flatMap((s) => parseStatus(s) ?? []);
}
