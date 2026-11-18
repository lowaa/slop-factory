import type { Ticket } from "@/api";
import type { TicketApiQuery } from "../ticket_query/ticket_query";

// What the browser can say about a ticket against a query. "needs_server"
// is for filters only the server can evaluate (text search) or that this
// code does not know, so an unknown param can never wrongly hide a ticket.
export type Verdict = "match" | "no_match" | "needs_server";

export type TicketMatcher = (ticket: Ticket, query: TicketApiQuery) => Verdict;

// Request params that say how to page, not which tickets.
const PAGING_PARAMS = new Set(["status", "limit", "cursor", "sort"]);
const DATE_FIELDS: Record<string, "created_at" | "updated_at" | "closed_at"> = {
  created: "created_at",
  updated: "updated_at",
  closed: "closed_at",
};

function list(value: string | readonly string[] | undefined): readonly string[] {
  return value === undefined ? [] : typeof value === "string" ? [value] : value;
}

// The server's handling of a range's end day is not pinned down in the
// query contract, so a ticket on either boundary day is left to the server
// instead of being guessed at.
function dateVerdict(stamp: string | null, from: string | undefined, to: string | undefined): Verdict {
  if (!from && !to) return "match";
  if (stamp === null) return "no_match";
  const day = stamp.slice(0, 10);
  if ((from && day < from) || (to && day > to)) return "no_match";
  if (day === from || day === to) return "needs_server";
  return "match";
}

export const matchTicket: TicketMatcher = (ticket, query) => {
  if (!query.status.includes(ticket.status)) return "no_match";
  const priorities = list(query.params.priority);
  if (priorities.length > 0 && !priorities.includes(ticket.priority)) return "no_match";

  let verdict: Verdict = "match";
  for (const [name, field] of Object.entries(DATE_FIELDS)) {
    const from = list(query.params[`${name}_from`])[0];
    const to = list(query.params[`${name}_to`])[0];
    const v = dateVerdict(ticket[field], from, to);
    if (v === "no_match") return "no_match";
    if (v === "needs_server") verdict = "needs_server";
  }

  const known = (p: string) => PAGING_PARAMS.has(p) || p === "priority" || /^(created|updated|closed)_(from|to)$/.test(p);
  for (const [name, value] of Object.entries(query.params)) {
    if (!known(name) && list(value).length > 0) verdict = "needs_server";
  }
  return verdict;
};
