import { FILTER_STATUSES, TicketFilterModel } from "@/domain/ticket_filter_model";

// Every kind of field the matcher reads, with the default statuses of the
// Unreviewed list (parked and cancelled hidden).
export const TEST_FILTER_MODEL = new TicketFilterModel("test", [
  { type: "text", id: "text", param: "q", label: "Text" },
  { type: "status", id: "status", param: "exclude", defaultExcluded: ["parked", "cancelled"] },
  { type: "priority", id: "priority", param: "priority" },
  { type: "date_range", id: "closed", fromParam: "from", toParam: "to", label: "Merged", dateField: "closed" },
]);
export const EVERY_STATUS_EXCLUDED = [...FILTER_STATUSES];
