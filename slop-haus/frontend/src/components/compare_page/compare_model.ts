import type { CompareMeasure, CompareRequest, CompareResult, ComparePair, CompareRow, CompareSummary } from "@/api";
import { TicketFilterModel } from "@/domain/ticket_filter_model";

// The default variant is the attempts routed to none; the server names it "".
export const DEFAULT_VARIANT = "";

// Fewer tickets than this and a median difference is noise. The server
// reports n and leaves judging it to the reader, so the page says so.
export const SMALL_N = 5;

// Done tickets only: a compare reads the original (promoted) attempt, which
// a ticket that is not done has not finished.
export const COMPARE_FILTER_MODEL = new TicketFilterModel("compare", [
  { type: "text", id: "text", param: "q", label: "Search tickets", placeholder: "Title, text, comments" },
  { type: "date_range", id: "closed", fromParam: "closed_from", toParam: "closed_to", label: "Merged", dateField: "closed" },
]);

export type SetSource = "batch" | "filter";

export interface ComparePick {
  source: SetSource;
  batchId: string;
  ticketIds: number[];
  // More tickets match the filter than were listed.
  ticketsTruncated: boolean;
  minLines: string;
  maxLines: string;
  baseline: string;
  challengers: string[];
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isBatchId(text: string): boolean {
  return UUID.test(text.trim());
}

function lines(text: string): number | undefined {
  const n = Number(text.trim());
  return text.trim() !== "" && Number.isInteger(n) && n >= 0 ? n : undefined;
}

// Why the comparison cannot be asked for yet; null when it can.
export function blockedReason(pick: ComparePick, maxTickets: number): string | null {
  if (pick.source === "batch" && !isBatchId(pick.batchId)) return "Enter a replay batch id.";
  if (pick.source === "filter" && pick.ticketIds.length === 0) return "No tickets match the filter.";
  if (pick.source === "filter" && (pick.ticketsTruncated || pick.ticketIds.length > maxTickets))
    return `More than ${maxTickets} tickets match; a comparison reads at most ${maxTickets}. Narrow the filter.`;
  if (pick.minLines.trim() !== "" && lines(pick.minLines) === undefined) return "The smallest size must be a whole number of lines.";
  if (pick.maxLines.trim() !== "" && lines(pick.maxLines) === undefined) return "The largest size must be a whole number of lines.";
  if (pick.challengers.length === 0) return "Pick at least one challenger.";
  if (pick.challengers.includes(pick.baseline)) return "The baseline cannot also be a challenger.";
  return null;
}

export function buildRequest(pick: ComparePick): CompareRequest {
  const min = lines(pick.minLines);
  const max = lines(pick.maxLines);
  return {
    ...(pick.source === "batch" ? { replay_batch_id: pick.batchId.trim() } : { ticket_ids: pick.ticketIds }),
    ...(min !== undefined ? { min_diff_lines: min } : {}),
    ...(max !== undefined ? { max_diff_lines: max } : {}),
    baseline: pick.baseline,
    challengers: pick.challengers,
  };
}

export function variantLabel(name: string): string {
  return name === DEFAULT_VARIANT ? "Default variant" : name;
}

export function smallNCaveat(tickets: number): string | null {
  if (tickets >= SMALL_N) return null;
  return `${tickets} ${tickets === 1 ? "ticket" : "tickets"}: too few to trust a difference`;
}

export interface MeasureGroup {
  group: CompareMeasure["group"];
  label: string;
  measures: CompareMeasure[];
}

const GROUPS: { group: CompareMeasure["group"]; label: string }[] = [
  { group: "cost", label: "Cost" },
  { group: "human_cost", label: "Human guidance" },
  { group: "quality", label: "Quality" },
  { group: "signals", label: "Signals" },
];

// The groups in a fixed order, never merged into one figure. A measure the
// pair has no difference for stays listed, as unknown rather than zero.
export function groupMeasures(measures: readonly CompareMeasure[]): MeasureGroup[] {
  return GROUPS.map(({ group, label }) => ({ group, label, measures: measures.filter((m) => m.group === group) })).filter(
    (g) => g.measures.length > 0,
  );
}

export function notCounted(measures: readonly CompareMeasure[]): CompareMeasure[] {
  return measures.filter((m) => m.group === "not_counted");
}

export function formatValue(value: number, unit: string, signed = false): string {
  const text = Number.isInteger(value) ? String(value) : value.toFixed(Math.abs(value) < 1 ? 3 : 2);
  const sign = signed && value > 0 ? "+" : "";
  return `${sign}${text}${unit ? ` ${unit}` : ""}`;
}

export function spreadText(s: CompareSummary, unit: string): string {
  return `IQR ${formatValue(s.q1, unit, true)} to ${formatValue(s.q3, unit, true)}; range ${formatValue(s.min, unit, true)} to ${formatValue(s.max, unit, true)}`;
}

export function pairRows(result: CompareResult, pair: ComparePair): CompareRow[] {
  return result.rows.filter((r) => r.challenger === pair.challenger);
}

// The runs per ticket on one side of a pair, as "1" or "1 to 3".
export function runsText(rows: readonly CompareRow[], side: "baseline" | "challenger_side"): string {
  if (rows.length === 0) return "0";
  const runs = rows.map((r) => r[side].runs);
  const lo = Math.min(...runs);
  const hi = Math.max(...runs);
  return lo === hi ? String(lo) : `${lo} to ${hi}`;
}
