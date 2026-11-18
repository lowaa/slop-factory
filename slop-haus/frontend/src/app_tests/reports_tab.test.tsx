import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient } from "@/api";
import { makeProfile, makeLlmUsageReport, makeReport, makeTicket, makeUsageSums } from "@/test/fixtures";
import { forgetRememberedFilters } from "@/components/ticket_filter";
import { FILTER_STATUSES } from "@/domain/ticket_filter_model";
import { serveTickets } from "@/test/serve_tickets";
import { mockOffsetSize } from "@/test/layout_mock";
import { renderApp, mockBoardShell } from "@/test/render_app";
import { headerNav, openMoreMenu } from "@/test/header";
import { FIRST_PAINT_TIMEOUT_MS } from "@/test/timeouts";

describe("reports tab", () => {
  it("takes its period and priority from the address, and puts a change back in it", async () => {
    window.history.pushState({}, "", "/reports?days=7&priority=high");
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile());
    mockBoardShell();
    const getReport = vi.spyOn(apiClient, "getReport").mockResolvedValue(makeReport());
    const user = userEvent.setup();

    renderApp();

    await screen.findByText(/Nothing was finished/);
    expect(getReport).toHaveBeenCalledWith({ days: 7, priority: "high" });
    // The tab is behind More, whose button reads as the page on show.
    expect(within(await headerNav()).getByRole("button", { name: "Reports" })).toBeInTheDocument();
    await openMoreMenu(user);
    expect(screen.getByRole("link", { name: "Reports" })).toHaveAttribute("aria-current", "page");
    await openMoreMenu(user);

    await user.click(screen.getByRole("button", { name: "30 days" }));
    // The default period is left out of the address.
    await waitFor(() => expect(window.location.search).toBe("?priority=high"));
    expect(getReport).toHaveBeenLastCalledWith({ days: 30, priority: "high" });

    await user.click(screen.getByRole("combobox", { name: "Priority" }));
    await user.click(screen.getByRole("option", { name: "urgent" }));
    await waitFor(() => expect(window.location.search).toBe("?priority=urgent"));
    expect(getReport).toHaveBeenLastCalledWith({ days: 30, priority: "urgent" });

    await user.click(screen.getByRole("combobox", { name: "Priority" }));
    await user.click(screen.getByRole("option", { name: "All priorities" }));
    await waitFor(() => expect(window.location.search).toBe(""));
    expect(getReport).toHaveBeenLastCalledWith({ days: 30, priority: undefined });
  });
});

describe("reports tab: the by-ticket usage filter", () => {
  const TENANT = "tenant-a";

  function mockReports() {
    vi.spyOn(apiClient, "me").mockResolvedValue(makeProfile({ tenant_id: TENANT }));
    mockBoardShell();
    vi.spyOn(apiClient, "getReport").mockResolvedValue(makeReport());
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(
      makeLlmUsageReport({ totals: [makeUsageSums({ transcripts: 1 })], tickets: 1 }),
    );
    return vi.spyOn(apiClient, "getLlmUsageTickets").mockResolvedValue({
      total: 1,
      tickets: [{ id: 7, title: "Zebra import", status: "done", modes: [makeUsageSums()], guidance_modes: [] }],
    });
  }

  function search(): Record<string, string> {
    return Object.fromEntries(new URLSearchParams(window.location.search));
  }

  afterEach(() => {
    forgetRememberedFilters();
  });

  it("opens with the filter its address names, beside the report's period and priority", async () => {
    window.history.pushState({}, "", "/reports?days=7&priority=high&q=zebra&exclude=parked&created_from=2026-01-02");
    const tickets = mockReports();

    renderApp();

    const box = await screen.findByRole("searchbox", { name: "Search tickets" });
    expect(box).toHaveValue("zebra");
    expect(screen.getByLabelText("Created from")).toHaveValue("2026-01-02");
    await waitFor(() => expect(tickets).toHaveBeenCalled(), { timeout: FIRST_PAINT_TIMEOUT_MS });
    expect(tickets).toHaveBeenLastCalledWith({
      days: 7,
      status: FILTER_STATUSES.filter((s) => s !== "parked"),
      priority: ["high"],
      q: "zebra",
      created_from: "2026-01-02",
      limit: 50,
    });
  });

  it("puts a change in the address, keeping the report's own filter, and a period change keeps this one", async () => {
    window.history.pushState({}, "", "/reports?days=7&priority=high");
    mockReports();
    const user = userEvent.setup();

    renderApp();
    fireEvent.change(await screen.findByLabelText("Created to"), { target: { value: "2026-03-01" } });

    await waitFor(() => expect(search()).toEqual({ days: "7", priority: "high", created_to: "2026-03-01" }));

    await user.click(screen.getByRole("button", { name: "30 days" }));
    await waitFor(() => expect(search()).toEqual({ priority: "high", created_to: "2026-03-01" }));
  });

  it("is kept by a ticket's Back link, with the period and priority", async () => {
    window.history.pushState({}, "", "/reports?days=7&priority=high&q=zebra");
    const tickets = mockReports();
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 7, title: "Zebra import" }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "listAgentContexts").mockResolvedValue([]);
    vi.spyOn(apiClient, "listRevisions").mockResolvedValue([]);
    vi.spyOn(apiClient, "listStatusHistory").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    // jsdom lays nothing out, so the list's window would be empty.
    mockOffsetSize({ height: 600, width: 600 });
    const user = userEvent.setup();

    renderApp();
    // The first paint of the windowed list and of the lazily loaded ticket
    // page are slow on a loaded runner, so those two get the long wait; the
    // steps after them find their targets under the default timeout.
    await user.click(await screen.findByRole("link", { name: /Zebra import/ }, { timeout: FIRST_PAINT_TIMEOUT_MS }));
    await user.click(
      await screen.findByRole("link", { name: "← Back to reports" }, { timeout: FIRST_PAINT_TIMEOUT_MS }),
    );

    await waitFor(() => expect(window.location.pathname).toBe("/reports"));
    expect(search()).toEqual({ days: "7", priority: "high", q: "zebra" });
    expect(await screen.findByRole("searchbox", { name: "Search tickets" })).toHaveValue("zebra");
    await waitFor(() =>
      expect(tickets).toHaveBeenLastCalledWith(expect.objectContaining({ days: 7, priority: ["high"], q: "zebra" })),
    );
  });

  it("is not the board's filter, nor does the board's reach it", async () => {
    window.history.pushState({}, "", "/reports?q=zebra");
    mockReports();
    serveTickets(() => []);
    const user = userEvent.setup();

    renderApp();
    await screen.findByRole("searchbox", { name: "Search tickets" });
    await user.click(screen.getByRole("link", { name: "Board" }));

    await waitFor(() => expect(window.location.pathname).toBe("/"));
    expect(await screen.findByRole("searchbox", { name: "Search tickets" })).toHaveValue("");
    expect(search()).toEqual({});
  });
});
