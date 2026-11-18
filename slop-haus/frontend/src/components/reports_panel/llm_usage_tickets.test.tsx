import { afterEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type LlmUsageTicket } from "@/api";
import { makeUsageSums } from "@/test/fixtures";
import { renderSignedIn } from "@/test/render_signed_in";
import { forgetRememberedFilters, rememberedFilterSearch } from "@/components/ticket_filter";
import { FILTER_STATUSES, type FilterValues } from "@/domain/ticket_filter_model";
import UsageTickets from "./llm_usage_tickets";
import { DEFAULT_USAGE_TICKETS_VALUES } from "./usage_tickets_filter";

afterEach(() => {
  vi.restoreAllMocks();
  forgetRememberedFilters();
});

function usageTicket(id: number): LlmUsageTicket {
  return { id, title: `ticket ${id}`, status: "done", modes: [makeUsageSums({ input_tokens: 1000 })], guidance_modes: [] };
}

const page = (ids: number[], total = ids.length, next_cursor?: string) => ({ total, next_cursor, tickets: ids.map(usageTicket) });

type Props = Parameters<typeof UsageTickets>[0];
type Change = (props: Partial<Props>) => void;

// Holds the props the way the page does, so a test changes them from outside
// as the address would.
function Harness({ initial, expose }: { initial: Partial<Props>; expose: (change: Change) => void }) {
  const [props, setProps] = useState<Partial<Props>>(initial);
  expose((next) => setProps((prev) => ({ ...prev, ...next })));
  return <UsageTickets days={30} values={DEFAULT_USAGE_TICKETS_VALUES} onFilterChange={() => {}} viewportHeight={2000} {...props} />;
}

async function renderList(props: Partial<Props> = {}) {
  let change: Change = () => {};
  const onFilterChange = vi.fn();
  const view = await renderSignedIn(
    <Harness initial={{ onFilterChange, ...props }} expose={(c) => (change = c)} />,
  );
  return { ...view, onFilterChange, change: (next: Partial<Props>) => act(() => change(next)) };
}

describe("UsageTickets", () => {
  it("shows a ticket's worker and guidance usage as separate labelled figures", async () => {
    vi.spyOn(apiClient, "getLlmUsageTickets").mockResolvedValue({
      total: 2,
      tickets: [
        {
          ...usageTicket(1),
          guidance_modes: [makeUsageSums({ billing_mode: "metered", input_tokens: 7000, cost_micros: 300_000 })],
        },
        { ...usageTicket(2), modes: [], guidance_modes: [makeUsageSums({ input_tokens: 5000 })] },
      ],
    });

    await renderList();

    const first = (await screen.findByRole("link", { name: /#1 ticket 1/ })).closest(".llm-usage__ticket") as HTMLElement;
    const worker = first.querySelector('[data-kind="worker"]')!;
    const guidance = first.querySelector('[data-kind="guidance"]')!;
    expect(worker).toHaveTextContent(/worker\s+Subscription\s+1k tokens/);
    expect(guidance).toHaveTextContent(/guidance\s+Metered\s+7k tokens/);
    const second = screen.getByRole("link", { name: /#2 ticket 2/ }).closest(".llm-usage__ticket") as HTMLElement;
    expect(second.querySelector('[data-kind="worker"]')).toBeNull();
    expect(second.querySelector('[data-kind="guidance"]')).toHaveTextContent("5k tokens");
  });

  it("asks for the filter's fields and the report's priority, in the shared ticket query's terms", async () => {
    const list = vi.spyOn(apiClient, "getLlmUsageTickets").mockResolvedValue(page([1]));
    const values: FilterValues = {
      text: "parser",
      status: ["parked", "cancelled"],
      created: { from: "2026-01-02", to: "2026-01-09" },
    };

    await renderList({ values, priority: "high" });

    await screen.findByRole("link", { name: /#1 ticket 1/ });
    expect(list).toHaveBeenCalledWith({
      days: 30,
      status: FILTER_STATUSES.filter((s) => s !== "parked" && s !== "cancelled"),
      priority: ["high"],
      q: "parser",
      created_from: "2026-01-02",
      created_to: "2026-01-09",
      limit: 50,
    });
  });

  it("hands a change of the filter to the page, which keeps it in the address", async () => {
    vi.spyOn(apiClient, "getLlmUsageTickets").mockResolvedValue(page([1]));
    const { onFilterChange } = await renderList();
    await screen.findByRole("link", { name: /#1 ticket 1/ });

    fireEvent.change(screen.getByLabelText("Created from"), { target: { value: "2026-02-01" } });

    expect(onFilterChange).toHaveBeenCalledWith({ ...DEFAULT_USAGE_TICKETS_VALUES, created: { from: "2026-02-01", to: "" } });
  });

  it("starts the list over for a changed filter, and never shows a late reply to the old one", async () => {
    let answerOld: (p: ReturnType<typeof page>) => void = () => {};
    const list = vi
      .spyOn(apiClient, "getLlmUsageTickets")
      .mockReturnValueOnce(new Promise((resolve) => (answerOld = resolve)))
      .mockResolvedValueOnce(page([2]));
    const { change } = await renderList();

    await change({ values: { ...DEFAULT_USAGE_TICKETS_VALUES, text: "two" } });
    await screen.findByRole("link", { name: /#2 ticket 2/ });
    answerOld(page([1]));
    await new Promise((r) => setTimeout(r, 0));

    expect(screen.queryByRole("link", { name: /#1 ticket 1/ })).not.toBeInTheDocument();
    expect(list).toHaveBeenLastCalledWith(expect.not.objectContaining({ cursor: expect.anything() }));
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ q: "two" }));
  });

  it("asks nothing when every status is unticked", async () => {
    const list = vi.spyOn(apiClient, "getLlmUsageTickets");

    await renderList({ values: { ...DEFAULT_USAGE_TICKETS_VALUES, status: [...FILTER_STATUSES] } });

    expect(await screen.findByText("No ticket with usage matches these filters.")).toBeInTheDocument();
    expect(list).not.toHaveBeenCalled();
  });

  it("says no ticket matches when a filter leaves none", async () => {
    vi.spyOn(apiClient, "getLlmUsageTickets").mockResolvedValue(page([]));

    await renderList({ values: { ...DEFAULT_USAGE_TICKETS_VALUES, text: "nothing" } });

    expect(await screen.findByText("No ticket with usage matches these filters.")).toBeInTheDocument();
  });

  it("says no ticket has usage when nothing filters", async () => {
    vi.spyOn(apiClient, "getLlmUsageTickets").mockResolvedValue(page([]));

    await renderList();

    expect(await screen.findByText("No ticket has usage in this period.")).toBeInTheDocument();
  });

  it("follows the report's priority: a different one is a new list", async () => {
    const list = vi.spyOn(apiClient, "getLlmUsageTickets").mockResolvedValueOnce(page([1])).mockResolvedValueOnce(page([2]));
    const { change } = await renderList({ priority: "low" });
    await screen.findByRole("link", { name: /#1 ticket 1/ });

    await change({ priority: "urgent" });

    await screen.findByRole("link", { name: /#2 ticket 2/ });
    expect(screen.queryByRole("link", { name: /#1 ticket 1/ })).not.toBeInTheDocument();
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ priority: ["urgent"] }));
  });

  it("remembers the whole address for the Back link, the report's filter with this one, under its own name", async () => {
    vi.spyOn(apiClient, "getLlmUsageTickets").mockResolvedValue(page([1]));

    await renderList({ values: { ...DEFAULT_USAGE_TICKETS_VALUES, text: "parser" }, priority: "high" });

    await waitFor(() => expect(rememberedFilterSearch("reports")).toEqual({ q: "parser", priority: "high" }));
    // Nothing of it reaches another page's filter.
    expect(rememberedFilterSearch("board")).toBeUndefined();
    expect(rememberedFilterSearch("unreviewed")).toBeUndefined();
  });

  it("keeps what is loaded when a later page fails, and retries that page", async () => {
    const list = vi
      .spyOn(apiClient, "getLlmUsageTickets")
      .mockResolvedValueOnce(page([1], 2, "c2"))
      .mockRejectedValueOnce(new ApiError(500, "boom"))
      .mockResolvedValueOnce(page([2], 2));
    await renderList();

    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
    expect(screen.getByRole("link", { name: /#1 ticket 1/ })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByRole("link", { name: /#2 ticket 2/ })).toBeInTheDocument();
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: "c2" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    // Neither a failed page nor its retry asks the same page twice on its own.
    expect(list).toHaveBeenCalledTimes(3);
  });
});
