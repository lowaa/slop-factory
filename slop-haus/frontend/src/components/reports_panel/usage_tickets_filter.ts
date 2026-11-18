import { TicketFilterModel, type FilterValues } from "@/domain/ticket_filter_model";

// The filter of Reports -> LLM usage -> By ticket, as the shared ticket
// filter widget holds it. Its address is ?q=, ?exclude=, ?created_from= and
// ?created_to=, next to the report's own ?days= and ?priority=.
//
// Priority and the period are not fields of this filter: the report already
// has a priority and a period, and a second control for either could
// disagree with it. The list takes both from the report's filter (see
// llm_usage_tickets.tsx), so the one control in the page sets what the
// list shows too. The date range is on the ticket's creation, which the
// report's period (when usage was last reported) says nothing about.
//
// Every status is listed by default: usage is by ticket whatever its state,
// and a ticket that was parked or cancelled after spending still spent it.
export const USAGE_TICKETS_FILTER_MODEL = new TicketFilterModel("reports", [
  { type: "text", id: "text", param: "q", label: "Search tickets", placeholder: "Title, text, comments" },
  { type: "status", id: "status", param: "exclude", defaultExcluded: [] },
  { type: "date_range", id: "created", fromParam: "created_from", toParam: "created_to", label: "Created", dateField: "created" },
]);

export const DEFAULT_USAGE_TICKETS_VALUES: FilterValues = USAGE_TICKETS_FILTER_MODEL.defaults();

export interface UsageTicketsSearch {
  q?: string;
  exclude?: string;
  created_from?: string;
  created_to?: string;
}

export function parseUsageTicketsSearch(raw: Record<string, unknown>): UsageTicketsSearch {
  return USAGE_TICKETS_FILTER_MODEL.parseSearch(raw);
}
