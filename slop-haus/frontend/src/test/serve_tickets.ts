import { vi } from "vitest";
import { apiClient, type Status, type Ticket } from "@/api";

type Filter = Parameters<typeof apiClient.listTicketsPage>[0];
export type Page = Awaited<ReturnType<typeof apiClient.listTicketsPage>>;

// A server for the board's per-column requests: it holds `tickets` (read on
// every request, so a test can change them) and answers each column's
// request with the tickets of its status that contain the search text.
export function serveTickets(tickets: () => Ticket[]) {
  // A work column's first page is the board's, which for tickets with one
  // attempt is the same list.
  vi.spyOn(apiClient, "listBoardColumn").mockImplementation((filter) => apiClient.listTicketsPage(filter));
  return vi.spyOn(apiClient, "listTicketsPage").mockImplementation(async (filter: Filter): Promise<Page> => {
    const text = (filter.text ?? "").toLowerCase();
    return {
      tickets: tickets().filter((t) => filter.statuses?.includes(t.status) && (!text || t.title.toLowerCase().includes(text))),
    };
  });
}

export function boardRequests(): Filter[] {
  return vi.mocked(apiClient.listTicketsPage).mock.calls.map(([filter]) => filter);
}

// The status each recent request was for, in the order asked.
export function lastRequestedStatuses(count: number): Status[] {
  return boardRequests().slice(-count).map((r) => r.statuses![0]);
}
