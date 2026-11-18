import { apiClient } from "@/api";
import { useResource } from "@/lib/resource/use_resource";
import type { DateRange, FilterValues } from "@/domain/ticket_filter_model";

// Done tickets the filter matches, up to what a comparison may read. The
// ticket list caps a page at that same number, so a next page is how "over
// the bound" is told from "exactly at it".
export function useFilteredTickets(values: FilterValues, maxTickets: number, enabled: boolean) {
  const text = values.text as string;
  const closed = values.closed as DateRange;
  return useResource(
    ["compare", "tickets", text, closed.from, closed.to, maxTickets],
    async () => {
      const page = await apiClient.listTicketsPage({
        statuses: ["done"],
        text,
        closedFrom: closed.from,
        closedTo: closed.to,
        limit: maxTickets,
      });
      return { ids: page.tickets.map((t) => t.id), truncated: page.nextCursor !== undefined };
    },
    { enabled, fallbackError: "Failed to load tickets." },
  );
}
