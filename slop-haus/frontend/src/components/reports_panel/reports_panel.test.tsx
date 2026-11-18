import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeReport, makeReportState } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import type { ReportFilter } from "./report_filter";
import ReportsPanel from "./reports_panel";
import { DEFAULT_USAGE_TICKETS_VALUES } from "./usage_tickets_filter";

// A caller that applies a filter change the way ReportsRoute does: the new
// filter comes back in as the panel's prop.
function Harness({ initial = { days: 30 } }: { initial?: ReportFilter }) {
  const [filter, setFilter] = useState<ReportFilter>(initial);
  const [ticketValues, setTicketValues] = useState(DEFAULT_USAGE_TICKETS_VALUES);
  return <ReportsPanel filter={filter} onChange={setFilter} ticketValues={ticketValues} onTicketFilterChange={setTicketValues} />;
}

function stateRow(name: string): HTMLElement {
  const row = document.querySelector<HTMLElement>(`[data-state="${name}"]`);
  if (!row) throw new Error(`no row for ${name}`);
  return row;
}

// The usage section has tests of its own; here it just never answers.
beforeEach(() => {
  vi.spyOn(apiClient, "getLlmUsageReport").mockReturnValue(new Promise(() => {}));
  vi.spyOn(apiClient, "getLockHistoryReport").mockReturnValue(new Promise(() => {}));
  vi.spyOn(apiClient, "getScopeCostReport").mockReturnValue(new Promise(() => {}));
  vi.spyOn(apiClient, "getShadowDecisionsReport").mockReturnValue(new Promise(() => {}));
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("ReportsPanel", () => {
  it("shows the LLM usage section for the chosen period, and asks again when it changes", async () => {
    vi.spyOn(apiClient, "getReport").mockResolvedValue(makeReport());
    const usage = vi.mocked(apiClient.getLlmUsageReport);

    await renderSignedIn(<Harness />);

    expect(await screen.findByRole("heading", { name: "LLM usage" })).toBeInTheDocument();
    expect(usage).toHaveBeenLastCalledWith({ days: 30 });
    await userEvent.click(screen.getByRole("button", { name: "7 days" }));
    expect(usage).toHaveBeenLastCalledWith({ days: 7 });
  });

  it("shows a loading note until the report arrives", async () => {
    vi.spyOn(apiClient, "getReport").mockReturnValue(new Promise(() => {}));

    await renderSignedIn(<Harness />);

    expect(screen.getByText("Loading report…")).toBeInTheDocument();
  });

  it("says so when the report took too long (503)", async () => {
    vi.spyOn(apiClient, "getReport").mockRejectedValue(new ApiError(503, "slow"));

    await renderSignedIn(<Harness />);

    expect(await screen.findByRole("alert")).toHaveTextContent("took too long");
  });

  it("shows the server's message for any other failure", async () => {
    vi.spyOn(apiClient, "getReport").mockRejectedValue(new ApiError(500, "database is down"));

    await renderSignedIn(<Harness />);

    expect(await screen.findByRole("alert")).toHaveTextContent("database is down");
  });

  it("falls back to a generic message when the failure is not an API error", async () => {
    vi.spyOn(apiClient, "getReport").mockRejectedValue(new Error("network down"));

    await renderSignedIn(<Harness />);

    expect(await screen.findByRole("alert")).toHaveTextContent("Failed to load the report.");
  });

  it("says nothing happened when there is history but no done tickets and no stays", async () => {
    vi.spyOn(apiClient, "getReport").mockResolvedValue(makeReport());

    await renderSignedIn(<Harness />);

    expect(await screen.findByText(/Nothing was finished/)).toBeInTheDocument();
    expect(screen.queryByText("Lead time")).not.toBeInTheDocument();
    expect(screen.queryByTestId("history-note")).not.toBeInTheDocument();
  });

  it("shows no figures at all, and no zeros, when no history has been recorded", async () => {
    vi.spyOn(apiClient, "getReport").mockResolvedValue(
      makeReport({ history_from: null, now: [{ status: "open", count: 3, without_history: 3 }] }),
    );

    await renderSignedIn(<Harness />);

    expect(await screen.findByTestId("history-note")).toHaveTextContent("no history yet");
    expect(screen.queryByText("Lead time")).not.toBeInTheDocument();
    expect(screen.queryByText("Time in each state")).not.toBeInTheDocument();
    expect(screen.queryByText("Throughput")).not.toBeInTheDocument();
    expect(screen.getByText(/3 have no recorded time/)).toBeInTheDocument();
  });

  it("says history starts mid-period, and still shows the figures", async () => {
    vi.spyOn(apiClient, "getReport").mockResolvedValue(
      makeReport({
        history_from: "2026-09-20T08:00:00Z",
        lead: { done: 1, created_to_done: { p50_seconds: 60, p90_seconds: 60 }, claimed_done: 0, claimed_to_done: null },
      }),
    );

    await renderSignedIn(<Harness />);

    expect(await screen.findByTestId("history-note")).toHaveTextContent(
      "No history before 2026-09-20: status changes are recorded from then on, so earlier time is not shown.",
    );
    expect(screen.getByText("Lead time")).toBeInTheDocument();
  });

  it("renders the figures, with discussion first and marked as waiting on a person", async () => {
    vi.spyOn(apiClient, "getReport").mockResolvedValue(
      makeReport({
        lead: {
          done: 4,
          created_to_done: { p50_seconds: 86400 + 3600, p90_seconds: 3 * 86400 },
          claimed_done: 3,
          claimed_to_done: { p50_seconds: 5400, p90_seconds: 7200 },
        },
        lead_by_week: [
          { week_start: "2026-09-21T00:00:00Z", done: 4, created_to_done: { p50_seconds: 100, p90_seconds: 200 }, claimed_done: 0, claimed_to_done: null },
        ],
        states: [
          makeReportState("open", { stays: 5, duration: { p50_seconds: 120, p90_seconds: 600 }, share: 0.25 }),
          makeReportState("discussion", {
            stays: 2,
            ongoing: 1,
            duration: { p50_seconds: 7200, p90_seconds: 14400 },
            share: 0.75,
          }),
          makeReportState("review"),
        ],
        throughput: [
          { day: "2026-09-21T00:00:00Z", done: 1 },
          { day: "2026-09-22T00:00:00Z", done: 3 },
        ],
        now: [{ status: "open", count: 2, without_history: 0 }],
      }),
    );

    await renderSignedIn(<Harness />);

    await screen.findByText("Lead time");
    expect(screen.getByText("Created to done").nextSibling).toHaveTextContent("1d 1hp90 3d");
    expect(screen.getByText("First claimed to done").nextSibling).toHaveTextContent("over 3 of 4 done tickets");
    expect(screen.getByRole("img", { name: /by week/ })).toBeInTheDocument();

    const rows = Array.from(document.querySelectorAll<HTMLElement>("[data-state]"));
    expect(rows.map((r) => r.dataset.state)).toEqual(["discussion", "open", "review"]);
    const discussion = stateRow("discussion");
    expect(discussion).toHaveClass("reports-panel__state--discussion");
    expect(within(discussion).getByText("waiting on a person")).toBeInTheDocument();
    expect(discussion).toHaveTextContent("2h");
    expect(discussion).toHaveTextContent("75% of time");
    expect(discussion).toHaveTextContent("includes 1 still in this state, counted up to now");
    expect(stateRow("open")).not.toHaveTextContent("still in this state");
    // A state with no stays has no figures to show, which is not zero.
    expect(stateRow("review")).toHaveTextContent("no stays");
    expect(stateRow("review")).not.toHaveTextContent("0s");

    expect(screen.getByText(/counted up to 2026-10-03/)).toBeInTheDocument();
    expect(screen.getByText("4", { selector: "strong" })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Tickets done per day" })).toBeInTheDocument();
  });

  it("asks again for the new period and priority when a filter changes", async () => {
    const user = userEvent.setup();
    const getReport = vi.spyOn(apiClient, "getReport").mockResolvedValue(makeReport());

    await renderSignedIn(<Harness />);
    await screen.findByText(/Nothing was finished/);
    expect(getReport).toHaveBeenLastCalledWith({ days: 30, priority: undefined });

    await user.click(screen.getByRole("button", { name: "7 days" }));
    await vi.waitFor(() => expect(getReport).toHaveBeenLastCalledWith({ days: 7, priority: undefined }));
    expect(screen.getByRole("button", { name: "7 days" })).toHaveAttribute("aria-pressed", "true");

    await user.click(screen.getByRole("combobox", { name: "Priority" }));
    await user.click(screen.getByRole("option", { name: "urgent" }));
    await vi.waitFor(() => expect(getReport).toHaveBeenLastCalledWith({ days: 7, priority: "urgent" }));
    expect(getReport).toHaveBeenCalledTimes(3);
  });

  describe("include parked", () => {
    it("is off by default, and flips the request when ticked", async () => {
      const user = userEvent.setup();
      const getReport = vi.spyOn(apiClient, "getReport").mockResolvedValue(makeReport());

      await renderSignedIn(<Harness />);
      await screen.findByText(/Nothing was finished/);
      const toggle = screen.getByRole("checkbox", { name: "Include parked" });
      expect(toggle).not.toBeChecked();
      expect(getReport.mock.lastCall?.[0].include_parked).toBeFalsy();

      await user.click(toggle);
      await vi.waitFor(() => expect(getReport).toHaveBeenLastCalledWith({ days: 30, priority: undefined, include_parked: true }));
      expect(screen.getByRole("checkbox", { name: "Include parked" })).toBeChecked();

      await user.click(screen.getByRole("checkbox", { name: "Include parked" }));
      await vi.waitFor(() => expect(getReport.mock.lastCall?.[0].include_parked).toBeFalsy());
    });

    it("says how many parked tickets are not shown, and nothing when none", async () => {
      vi.spyOn(apiClient, "getReport").mockResolvedValue(makeReport({ parked_not_shown: 4 }));
      await renderSignedIn(<Harness />);
      expect(await screen.findByTestId("parked-not-shown")).toHaveTextContent("4 parked not shown");
    });

    it("has no note when no parked tickets are left out", async () => {
      vi.spyOn(apiClient, "getReport").mockResolvedValue(makeReport());
      await renderSignedIn(<Harness initial={{ days: 30, includeParked: true }} />);
      await screen.findByText(/Nothing was finished/);
      expect(screen.queryByTestId("parked-not-shown")).not.toBeInTheDocument();
    });

    it("asks for the parked status's waiting list only with it included", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getReport").mockResolvedValue(
        makeReport({ now: [{ status: "parked", count: 2, without_history: 0 }] }),
      );
      const getWaiting = vi
        .spyOn(apiClient, "getReportWaiting")
        .mockResolvedValue({ total: 0, tickets: [] });
      await renderSignedIn(<Harness initial={{ days: 30, includeParked: true }} />);
      await user.click(await screen.findByRole("button", { name: /Parked/ }));
      await vi.waitFor(() =>
        expect(getWaiting).toHaveBeenLastCalledWith({ status: "parked", priority: undefined, include_parked: true, limit: 10, offset: 0 }),
      );
    });
  });

  describe("where tickets are now", () => {
    const waiting = (from: number, count: number, total: number) => ({
      total,
      tickets: Array.from({ length: count }, (_, i) => ({
        id: from + i,
        title: `Ticket ${from + i}`,
        priority: "normal",
        since: "2026-09-01T00:00:00Z",
      })),
    });

    it("pages through the longest-waiting tickets of a status until all are shown", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getReport").mockResolvedValue(
        makeReport({ now: [{ status: "discussion", count: 12, without_history: 0 }] }),
      );
      const getWaiting = vi
        .spyOn(apiClient, "getReportWaiting")
        .mockResolvedValueOnce(waiting(1, 10, 12))
        .mockResolvedValueOnce(waiting(11, 2, 12));

      await renderSignedIn(<Harness initial={{ days: 30, priority: "high" }} />);
      await user.click(await screen.findByRole("button", { name: /Discussion/ }));

      expect(await screen.findByText("Showing 10 of 12, longest waiting first.")).toBeInTheDocument();
      expect(getWaiting).toHaveBeenLastCalledWith({ status: "discussion", priority: "high", limit: 10, offset: 0 });
      const link = screen.getByRole("link", { name: /Ticket 1$/ });
      expect(link.getAttribute("href")).toContain("from=reports");

      await user.click(screen.getByRole("button", { name: "Show more" }));

      expect(await screen.findByText("Showing 12 of 12, longest waiting first.")).toBeInTheDocument();
      expect(getWaiting).toHaveBeenLastCalledWith({ status: "discussion", priority: "high", limit: 10, offset: 10 });
      expect(screen.getAllByRole("link", { name: /Ticket \d+$/ })).toHaveLength(12);
      expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
    });

    it("shows a ticket once when the list moved between pages", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getReport").mockResolvedValue(
        makeReport({ now: [{ status: "open", count: 3, without_history: 0 }] }),
      );
      // Ticket 2 slid onto the second page when another ticket joined the first.
      vi.spyOn(apiClient, "getReportWaiting")
        .mockResolvedValueOnce(waiting(1, 2, 3))
        .mockResolvedValueOnce(waiting(2, 2, 3));

      await renderSignedIn(<Harness />);
      await user.click(await screen.findByRole("button", { name: /Open/ }));
      await screen.findByRole("link", { name: /Ticket 1$/ });
      await user.click(screen.getByRole("button", { name: "Show more" }));

      expect(await screen.findByRole("link", { name: /Ticket 3$/ })).toBeInTheDocument();
      expect(screen.getAllByRole("link", { name: /Ticket \d+$/ })).toHaveLength(3);
    });

    it("retries a failed page at the same offset", async () => {
      const user = userEvent.setup();
      vi.spyOn(apiClient, "getReport").mockResolvedValue(
        makeReport({ now: [{ status: "open", count: 1, without_history: 0 }] }),
      );
      const getWaiting = vi
        .spyOn(apiClient, "getReportWaiting")
        .mockRejectedValueOnce(new ApiError(500, "boom"))
        .mockResolvedValueOnce(waiting(1, 1, 1));

      await renderSignedIn(<Harness />);
      await user.click(await screen.findByRole("button", { name: /Open/ }));
      expect(await screen.findByRole("alert")).toHaveTextContent("boom");

      await user.click(screen.getByRole("button", { name: "Try again" }));

      expect(await screen.findByRole("link", { name: /Ticket 1$/ })).toBeInTheDocument();
      expect(getWaiting).toHaveBeenLastCalledWith({ status: "open", priority: undefined, limit: 10, offset: 0 });
    });

    it("lists the statuses in board order with their counts", async () => {
      vi.spyOn(apiClient, "getReport").mockResolvedValue(
        makeReport({
          now: [
            { status: "review", count: 1, without_history: 0 },
            { status: "in_progress", count: 4, without_history: 0 },
            { status: "open", count: 2, without_history: 1 },
          ],
        }),
      );

      await renderSignedIn(<Harness />);

      const toggles = await screen.findAllByRole("button", { name: /Open|In progress|Review/ });
      expect(toggles.map((t) => t.textContent)).toEqual(["Open2", "In progress4", "Review1"]);
      expect(screen.getByText(/1 has no recorded time/)).toBeInTheDocument();
    });
  });
});
