import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, within } from "storybook/test";
import { ApiError, type LlmUsageTicket } from "@/api";
import { stubApi } from "@/test/story_harness";
import { FILTER_STATUSES, type FilterValues } from "@/domain/ticket_filter_model";
import UsageTickets, { USAGE_TICKETS_PAGE_SIZE } from "./llm_usage_tickets";
import { DEFAULT_USAGE_TICKETS_VALUES, USAGE_TICKETS_FILTER_MODEL } from "./usage_tickets_filter";
import "./llm_usage.scss";

function usageTickets(count: number, firstId = 1): LlmUsageTicket[] {
  return Array.from({ length: count }, (_, i) => ({
    id: firstId + i,
    title: `Usage ticket ${firstId + i}`,
    status: "done",
    guidance_modes: [],
    modes: [
      {
        billing_mode: "subscription",
        input_tokens: 120_000,
        output_tokens: 8_000,
        cache_read_tokens: 900_000,
        cache_write_tokens: 40_000,
        reasoning_tokens: 0,
        cost_micros: 2_500_000,
        records: 4,
        requests: 12,
        unpriced_records: 0,
        unpriced_requests: 0,
      },
    ],
  }));
}

const requests: unknown[] = [];

const meta = {
  title: "Panels/ReportsPanel/UsageTickets",
  component: UsageTickets,
  // The window is pinned so the rows a story checks are the ones drawn.
  args: { days: 30, values: DEFAULT_USAGE_TICKETS_VALUES, onFilterChange: fn(), viewportHeight: 600 },
} satisfies Meta<typeof UsageTickets>;

export default meta;
type Story = StoryObj<typeof meta>;

export const OnePage: Story = {
  beforeEach: () => {
    requests.length = 0;
    stubApi({
      getLlmUsageTickets: async (params) => {
        requests.push(params);
        return { tickets: usageTickets(3), total: 3 };
      },
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByText("Showing 3 of 3, most tokens first."),
    ).toBeVisible();
    await expect(
      canvas.getByRole("link", { name: /Usage ticket 3\b/ }),
    ).toBeVisible();
    await expect(requests).toEqual([
      {
        days: 30,
        status: FILTER_STATUSES,
        limit: USAGE_TICKETS_PAGE_SIZE,
      },
    ]);
    // The row carries the status, billing mode, token total and cost kind.
    const row = canvas
      .getByRole("link", { name: /Usage ticket 1\b/ })
      .closest("li") as HTMLElement;
    await expect(row).toHaveTextContent("Done");
    await expect(row).toHaveTextContent(/Subscription\s+1\.1M tokens/);
    await expect(row).toHaveTextContent("estimated");
  },
};

export const Empty: Story = {
  beforeEach: () =>
    stubApi({ getLlmUsageTickets: async () => ({ tickets: [], total: 0 }) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByText("No ticket has usage in this period."),
    ).toBeVisible();
    await expect(canvas.queryByRole("button")).toBeNull();
  },
};

// The server's cursor, not an offset, says where the next page starts; the
// list asks for it as its end comes into view.
export const NextPageOnScroll: Story = {
  beforeEach: () => {
    requests.length = 0;
    stubApi({
      getLlmUsageTickets: async (params) => {
        requests.push(params);
        const second = params.cursor === "page-2";
        return {
          tickets: usageTickets(USAGE_TICKETS_PAGE_SIZE, second ? USAGE_TICKETS_PAGE_SIZE + 1 : 1),
          total: USAGE_TICKETS_PAGE_SIZE * 2,
          next_cursor: second ? undefined : "page-2",
        };
      },
    });
  },
  // Rows tall enough to need scrolling to the end of the first page.
  args: { viewportHeight: 300 },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByRole("link", { name: /Usage ticket 1\b/ }),
    ).toBeVisible();
    const viewport = canvas.getByRole("region", { name: "LLM usage by ticket" });
    viewport.scrollTop = viewport.scrollHeight;
    await expect(
      await canvas.findByRole("link", { name: /Usage ticket 51\b/ }),
    ).toBeInTheDocument();
    await expect(requests.at(-1)).toMatchObject({ cursor: "page-2" });
    await expect(requests[0]).not.toHaveProperty("cursor");
  },
};

// The filter widget is the ticket filter the other lists use; a change is
// handed to the page, which keeps it in the address.
export const Filtering: Story = {
  render: function Render(args) {
    const [values, setValues] = useState<FilterValues>(args.values);
    return <UsageTickets {...args} values={values} onFilterChange={setValues} />;
  },
  beforeEach: () => {
    requests.length = 0;
    stubApi({
      getLlmUsageTickets: async (params) => {
        requests.push(params);
        return params.q === "nothing"
          ? { tickets: [], total: 0 }
          : { tickets: usageTickets(2), total: 2 };
      },
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByRole("link", { name: /Usage ticket 1\b/ }),
    ).toBeVisible();
    await userEvent.type(
      canvas.getByRole("searchbox", { name: "Search tickets" }),
      "nothing",
    );
    await expect(
      await canvas.findByText("No ticket with usage matches these filters."),
    ).toBeVisible();
    await expect(requests.at(-1)).toMatchObject({ q: "nothing" });
    await userEvent.click(canvas.getByRole("button", { name: "Clear filters" }));
    await expect(
      await canvas.findByRole("link", { name: /Usage ticket 1\b/ }),
    ).toBeVisible();
  },
};

// A ticket with worker usage and guidance shows them as two labelled
// figures; one with guidance only has no worker figure.
export const WorkerAndGuidance: Story = {
  beforeEach: () => {
    const [both, only] = usageTickets(2);
    const guided = (tokens: number) => ({ ...both.modes[0], input_tokens: tokens, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, cost_micros: 700_000 });
    stubApi({
      getLlmUsageTickets: async () => ({
        tickets: [
          { ...both, guidance_modes: [guided(30_000)] },
          { ...only, modes: [], guidance_modes: [guided(5_000)] },
        ],
        total: 2,
      }),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const first = (await canvas.findByRole("link", { name: /Usage ticket 1\b/ })).closest(".llm-usage__ticket") as HTMLElement;
    await expect(within(first).getByText("worker")).toBeVisible();
    await expect(within(first).getByText("guidance")).toBeVisible();
    await expect(within(first).getByText(/30k tokens/)).toBeVisible();
    const second = canvas.getByRole("link", { name: /Usage ticket 2\b/ }).closest(".llm-usage__ticket") as HTMLElement;
    await expect(within(second).queryByText("worker")).toBeNull();
    await expect(within(second).getByText("guidance")).toBeVisible();
  },
};

export const Filtered: Story = {
  args: {
    priority: "high",
    values: USAGE_TICKETS_FILTER_MODEL.fromSearch({
      q: "parser",
      exclude: "parked,cancelled",
      created_from: "2026-10-01",
    }),
  },
  beforeEach: () => {
    requests.length = 0;
    stubApi({
      getLlmUsageTickets: async (params) => {
        requests.push(params);
        return { tickets: usageTickets(1), total: 1 };
      },
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByRole("link", { name: /Usage ticket 1\b/ }),
    ).toBeVisible();
    await expect(canvas.getByRole("searchbox", { name: "Search tickets" })).toHaveValue("parser");
    await expect(canvas.getByLabelText("Created from")).toHaveValue("2026-10-01");
    await expect(requests[0]).toMatchObject({
      priority: ["high"],
      q: "parser",
      created_from: "2026-10-01",
    });
  },
};

export const TryAgain: Story = {
  beforeEach: () => {
    let calls = 0;
    stubApi({
      getLlmUsageTickets: async () => {
        calls += 1;
        if (calls === 1) throw new ApiError(500, "Usage is unavailable.");
        return { tickets: usageTickets(2), total: 2 };
      },
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "Usage is unavailable.",
    );
    await userEvent.click(canvas.getByRole("button", { name: "Try again" }));
    await expect(
      await canvas.findByText("Showing 2 of 2, most tokens first."),
    ).toBeVisible();
    await expect(canvas.queryByRole("alert")).toBeNull();
  },
};
