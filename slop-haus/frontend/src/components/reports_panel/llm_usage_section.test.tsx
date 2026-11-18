import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { makeLlmUsageReport, makeUsageSums } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { FILTER_STATUSES } from "@/domain/ticket_filter_model";
import LlmUsageSection from "./llm_usage_section";
import { DEFAULT_USAGE_TICKETS_VALUES } from "./usage_tickets_filter";

afterEach(() => {
  vi.restoreAllMocks();
});

const TICKETS = { values: DEFAULT_USAGE_TICKETS_VALUES, onFilterChange: () => {}, viewportHeight: 2000 };
const render = () => renderSignedIn(<LlmUsageSection days={7} tickets={TICKETS} />);

describe("LlmUsageSection", () => {
  it("shows the guidance charged to tickets as its own line, apart from what is tied to no ticket", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(
      makeLlmUsageReport({
        totals: [makeUsageSums({ transcripts: 1, requests: 10, input_tokens: 1000, cost_micros: 2_000_000 })],
        unattributed: [makeUsageSums({ requests: 7, input_tokens: 900, cost_micros: 1_500_000 })],
        guidance: [makeUsageSums({ requests: 3, input_tokens: 100, cost_micros: 500_000 })],
      }),
    );

    await render();

    expect(await screen.findByTestId("guidance")).toHaveTextContent("3 requests, 100 tokens, $0.50 estimated");
    expect(screen.getByTestId("unattributed")).toHaveTextContent("Not tied to a ticket: 7 requests, 900 tokens, $1.50 estimated");
  });

  it("shows no guidance line when nothing was charged", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(
      makeLlmUsageReport({ totals: [makeUsageSums({ transcripts: 1 })] }),
    );

    await render();

    await screen.findByText("Totals");
    expect(screen.queryByTestId("guidance")).not.toBeInTheDocument();
  });

  it("shows a loading note until the report arrives", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockReturnValue(new Promise(() => {}));

    await render();

    expect(screen.getByText("Loading LLM usage…")).toBeInTheDocument();
  });

  it("says so when the report took too long, and shows other failures as they are", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockRejectedValueOnce(new ApiError(503, "slow"));
    const { unmount } = await render();
    expect(await screen.findByRole("alert")).toHaveTextContent("took too long");
    unmount();

    vi.spyOn(apiClient, "getLlmUsageReport").mockRejectedValueOnce(new ApiError(500, "database is down"));
    const second = await render();
    expect(await screen.findByRole("alert")).toHaveTextContent("database is down");
    second.unmount();

    vi.spyOn(apiClient, "getLlmUsageReport").mockRejectedValueOnce(new Error("network down"));
    await render();
    expect(await screen.findByRole("alert")).toHaveTextContent("Failed to load LLM usage.");
  });

  it("says nothing was reported when there is no usage, with no zeros", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(makeLlmUsageReport());

    await render();

    expect(await screen.findByText("No LLM usage was reported in this period.")).toBeInTheDocument();
    expect(screen.queryByText("Totals")).not.toBeInTheDocument();
  });

  it("shows each billing mode on its own, labelled, and never as one sum", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(
      makeLlmUsageReport({
        totals: [
          makeUsageSums({ billing_mode: "metered", transcripts: 2, requests: 30, input_tokens: 1500, cost_micros: 2_500_000 }),
          makeUsageSums({ billing_mode: "subscription", transcripts: 5, requests: 120, input_tokens: 2_000_000, cost_micros: 12_340_000 }),
        ],
      }),
    );

    await render();

    const metered = (await screen.findByText("cost is spent")).closest("li") as HTMLElement;
    const subscription = screen.getByText("cost is estimated").closest("li") as HTMLElement;
    expect(within(metered).getByText("$2.50")).toBeInTheDocument();
    expect(within(metered).getByText("spent")).toBeInTheDocument();
    expect(within(subscription).getByText("$12.34")).toBeInTheDocument();
    expect(within(subscription).getByText("estimated")).toBeInTheDocument();
    expect(screen.queryByText("$14.84")).not.toBeInTheDocument();
    expect(screen.getByTestId("llm-usage-rules")).toHaveTextContent("never added together");
  });

  it("shows usage with no price as unpriced, not as zero, and what a cost leaves out", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(
      makeLlmUsageReport({
        totals: [
          makeUsageSums({ billing_mode: "metered", transcripts: 1, requests: 4, unpriced_records: 1, unpriced_requests: 4 }),
          makeUsageSums({ billing_mode: "subscription", transcripts: 3, requests: 9, cost_micros: 1_000_000, unpriced_records: 2, unpriced_requests: 5 }),
        ],
      }),
    );

    await render();

    const metered = (await screen.findByText("cost is spent")).closest("li") as HTMLElement;
    expect(within(metered).getByText("unpriced")).toBeInTheDocument();
    expect(within(metered).queryByText(/\$0/)).not.toBeInTheDocument();
    const subscription = screen.getByText("cost is estimated").closest("li") as HTMLElement;
    expect(subscription).toHaveTextContent("$1.00 estimated (2 records, 5 requests, with no price: not in the cost)");
  });

  it("shows usage not tied to a ticket as its own line", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(
      makeLlmUsageReport({
        totals: [makeUsageSums({ transcripts: 3, requests: 12, cost_micros: 3_000_000 })],
        unattributed: [makeUsageSums({ requests: 5, input_tokens: 400, cost_micros: 1_000_000 })],
      }),
    );

    await render();

    expect(await screen.findByTestId("unattributed")).toHaveTextContent("Not tied to a ticket: 5 requests, 400 tokens, $1.00 estimated");
  });

  it("breaks usage down by agent and model, per billing mode, and tool use with an error rate", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(
      makeLlmUsageReport({
        totals: [makeUsageSums({ transcripts: 1, requests: 10 })],
        agents: [
          { agent_id: "a1", name: "alice", ...makeUsageSums({ requests: 7, input_tokens: 1000, cost_micros: 5_000_000 }) },
          { agent_id: "a2", name: "", ...makeUsageSums({ billing_mode: "metered", requests: 3, cost_micros: 100_000 }) },
        ],
        models: [{ provider: "anthropic", model: "claude-opus", ...makeUsageSums({ requests: 10 }) }],
        tools: [{ category: "bash", calls: 200, errors: 5, duration_ms: 125_000 }],
      }),
    );

    await render();

    const agents = (await screen.findByRole("columnheader", { name: "Agent" })).closest("table") as HTMLElement;
    const alice = within(agents).getByRole("rowheader", { name: "alice" }).closest("tr") as HTMLElement;
    expect(within(alice).getByText("Subscription")).toBeInTheDocument();
    expect(within(alice).getByText("$5.00")).toBeInTheDocument();
    expect(within(alice).getByText("1k")).toBeInTheDocument();
    expect(within(agents).getByRole("rowheader", { name: "(removed agent)" }).closest("tr")).toHaveTextContent("Metered");
    expect(within(agents).getByText("$0.10")).toBeInTheDocument();

    const models = screen.getByRole("columnheader", { name: "Model" }).closest("table") as HTMLElement;
    expect(within(models).getByRole("rowheader", { name: /claude-opus/ })).toBeInTheDocument();

    const tools = screen.getByRole("columnheader", { name: "Category" }).closest("table") as HTMLElement;
    const bash = within(tools).getByRole("rowheader", { name: "bash" }).closest("tr") as HTMLElement;
    expect(bash).toHaveTextContent("2002.5%2m 5s");
  });

  it("shows cost per done ticket next to the tickets, each a link, and pages them", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(
      makeLlmUsageReport({
        totals: [makeUsageSums({ transcripts: 1, requests: 10 })],
        tickets: 12,
        done_tickets: [
          { billing_mode: "subscription", tickets: 4, cost_micros: 8_000_000, cost_p50_micros: 1_500_000, unpriced_tickets: 1 },
        ],
      }),
    );
    const page = (ids: number[], total: number, next_cursor?: string) => ({
      total,
      next_cursor,
      tickets: ids.map((id) => ({ id, title: `ticket ${id}`, status: "done" as const, modes: [makeUsageSums({ input_tokens: 1000 * id, cost_micros: id * 100_000 })], guidance_modes: [] })),
    });
    const tickets = vi
      .spyOn(apiClient, "getLlmUsageTickets")
      .mockResolvedValueOnce(page([1, 2], 3, "c2"))
      .mockResolvedValueOnce(page([2, 3], 3));

    await render();

    expect(await screen.findByTestId("done-tickets")).toHaveTextContent(
      "Subscription: 4 done tickets with usage, median $1.50 estimated each, $8.00 estimated in all (1 with usage that has no price, so understated)",
    );
    const link = await screen.findByRole("link", { name: /#1 ticket 1/ });
    expect(link).toHaveAttribute("href", expect.stringContaining("/tickets/1"));
    // Every status is asked for, by name: the server's own default never decides.
    expect(tickets).toHaveBeenNthCalledWith(1, {
      days: 7,
      status: [...FILTER_STATUSES],
      limit: 50,
    });

    // The next page is asked for by the cursor the server gave, not an offset.
    expect(await screen.findByRole("link", { name: /#3 ticket 3/ })).toBeInTheDocument();
    expect(tickets).toHaveBeenLastCalledWith(expect.objectContaining({ days: 7, cursor: "c2" }));
    expect(tickets).toHaveBeenCalledTimes(2);
    expect(screen.getAllByRole("link", { name: /#2 ticket 2/ })).toHaveLength(1);
    expect(screen.getByText("Showing 3 of 3, most tokens first.")).toBeInTheDocument();
    const first = screen.getByRole("link", { name: /#1 ticket 1/ }).closest("li") as HTMLElement;
    expect(first).toHaveTextContent("Done");
    expect(first).toHaveTextContent("Subscription 1k tokens, $0.10 estimated");
  });

  it("draws a column for every day of the period, quiet ones too, one cost chart per billing mode", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(
      makeLlmUsageReport({
        from: "2026-09-30T12:00:00Z",
        to: "2026-10-03T12:00:00Z",
        totals: [makeUsageSums({ transcripts: 1 }), makeUsageSums({ billing_mode: "metered", transcripts: 1 })],
        days: [
          { day: "2026-09-30T00:00:00Z", ...makeUsageSums({ input_tokens: 1000, cost_micros: 2_000_000 }) },
          { day: "2026-10-02T00:00:00Z", ...makeUsageSums({ input_tokens: 3000, cost_micros: 1_000_000 }) },
          { day: "2026-10-02T00:00:00Z", ...makeUsageSums({ billing_mode: "metered", requests: 1 }) },
        ],
      }),
    );

    await render();

    const tokens = await screen.findByRole("img", { name: "Tokens per day" });
    expect(tokens.querySelectorAll(".column-chart__column")).toHaveLength(4);
    const titles = Array.from(tokens.querySelectorAll(".column-chart__column")).map((c) => c.getAttribute("title"));
    expect(titles).toEqual(["2026-09-30: 1k tokens", "2026-10-01: 0 tokens", "2026-10-02: 3k tokens", "2026-10-03: 0 tokens"]);
    const subscription = screen.getByRole("img", { name: "Subscription cost per day, estimated" });
    expect(subscription.querySelector(".column-chart__column")).toHaveAttribute("title", "2026-09-30: $2.00 estimated");
    const metered = screen.getByRole("img", { name: "Metered cost per day, spent" });
    expect(metered.querySelectorAll(".column-chart__column")[2]).toHaveAttribute("title", "2026-10-02: no priced usage");
  });

  it("offers a retry when a page of tickets fails", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(
      makeLlmUsageReport({ totals: [makeUsageSums({ transcripts: 1 })], tickets: 1 }),
    );
    vi.spyOn(apiClient, "getLlmUsageTickets")
      .mockRejectedValueOnce(new ApiError(500, "boom"))
      .mockResolvedValueOnce({ total: 1, tickets: [{ id: 9, title: "nine", status: "open", modes: [makeUsageSums()], guidance_modes: [] }] });

    await render();

    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("link", { name: /#9 nine/ })).toBeInTheDocument();
  });
});

describe("LlmUsageSection over-counted usage", () => {
  it("says some usage is left out and offers to include it, then asks the server for it", async () => {
    const spy = vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(
      makeLlmUsageReport({ totals: [makeUsageSums({ transcripts: 1 })], unreliable_records: 3 }),
    );

    await render();

    expect(await screen.findByTestId("overcount-notice")).toHaveTextContent("Some usage up to 2026-10-22 was over-counted and is left out");
    expect(spy).toHaveBeenLastCalledWith({ days: 7 });

    await userEvent.click(screen.getByRole("checkbox", { name: "Include over-counted usage" }));

    expect(await screen.findByText(/that was over-counted is included/)).toBeInTheDocument();
    expect(spy).toHaveBeenLastCalledWith({ days: 7, include_unreliable: true });
    expect(screen.getByRole("checkbox", { name: "Include over-counted usage" })).toBeChecked();
  });

  it("shows no notice when the period holds none", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(makeLlmUsageReport({ totals: [makeUsageSums({ transcripts: 1 })] }));

    await render();

    await screen.findByText("Totals");
    expect(screen.queryByTestId("overcount-notice")).not.toBeInTheDocument();
  });

  it("still shows the notice when everything in the period was left out", async () => {
    vi.spyOn(apiClient, "getLlmUsageReport").mockResolvedValue(makeLlmUsageReport({ unreliable_records: 2 }));

    await render();

    expect(await screen.findByTestId("overcount-notice")).toBeInTheDocument();
    expect(screen.getByText("No LLM usage was reported in this period.")).toBeInTheDocument();
  });
});
