// The tabs of a ticket's detail view, in the order they are shown. The
// route reads ?view= against this list, so a link can open a ticket on a
// given tab; "main" is the default and is left out of the URL.
export const TICKET_VIEWS = ["main", "agent-context"] as const;

export type TicketView = (typeof TICKET_VIEWS)[number];

export const TICKET_VIEW_LABELS: Record<TicketView, string> = {
  main: "Main",
  "agent-context": "Agent context history",
};
