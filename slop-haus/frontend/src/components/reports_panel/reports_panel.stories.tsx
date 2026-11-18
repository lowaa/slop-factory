import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { makeLlmUsageReport, makeReport, makeReportState, makeLockHistoryReport,
  makeScopeCostReport, makeShadowDecisionsReport } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import type { ReportFilter } from "./report_filter";
import ReportsPanel from "./reports_panel";
import { DEFAULT_USAGE_TICKETS_VALUES } from "./usage_tickets_filter";

// Applies a filter change the way ReportsRoute does: the new filter comes
// back in as the panel's prop.
function Stateful({ initial }: { initial: ReportFilter }) {
  const [filter, setFilter] = useState(initial);
  const [ticketValues, setTicketValues] = useState(DEFAULT_USAGE_TICKETS_VALUES);
  return <ReportsPanel filter={filter} onChange={setFilter} ticketValues={ticketValues} onTicketFilterChange={setTicketValues} />;
}

const meta = {
  title: "Panels/ReportsPanel",
  component: Stateful,
  // The tables below the filters have no controls of their own; the usage
  // section and "where now" have their own tickets, so they only render.
  beforeEach: () =>
    stubApi({
      getReport: async () => makeReport(),
      getLlmUsageReport: async () => makeLlmUsageReport(),
      getScopeCostReport: async () => makeScopeCostReport(),
      getLockHistoryReport: async () => makeLockHistoryReport(),
      getShadowDecisionsReport: async () => makeShadowDecisionsReport(),
    }),
} satisfies Meta<typeof Stateful>;

export default meta;
type Story = StoryObj<typeof meta>;

async function expectPeriodPressed(canvasElement: HTMLElement, name: string) {
  const canvas = within(canvasElement);
  await expect(await canvas.findByRole("button", { name })).toHaveAttribute("aria-pressed", "true");
  // Exactly one period is selected.
  await expect(canvas.getAllByRole("button", { pressed: true })).toHaveLength(1);
}

export const Period7Days: Story = {
  args: { initial: { days: 7 } },
  play: ({ canvasElement }) => expectPeriodPressed(canvasElement, "7 days"),
};

export const Period30Days: Story = {
  args: { initial: { days: 30 } },
  play: ({ canvasElement }) => expectPeriodPressed(canvasElement, "30 days"),
};

export const Period90Days: Story = {
  args: { initial: { days: 90 } },
  play: ({ canvasElement }) => expectPeriodPressed(canvasElement, "90 days"),
};

export const AllPriorities: Story = {
  args: { initial: { days: 30 } },
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("combobox", { name: "Priority" })).toHaveTextContent("All priorities");
  },
};

export const OnePriority: Story = {
  args: { initial: { days: 30, priority: "urgent" } },
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("combobox", { name: "Priority" })).toHaveTextContent("urgent");
  },
};

export const ChoosingFilters: Story = {
  args: { initial: { days: 30 } },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "90 days" }));
    await expectPeriodPressed(canvasElement, "90 days");
    const select = canvas.getByRole("combobox", { name: "Priority" });
    await userEvent.click(select);
    await userEvent.click(within(document.body).getByRole("option", { name: "high" }));
    await expect(select).toHaveTextContent("high");
  },
};

// On a phone the trigger is a finger-sized target and the open list stays
// inside the screen.
export const PhoneChoosingPriority: Story = {
  globals: { viewport: { value: "mobile1", isRotated: false } },
  args: { initial: { days: 30 } },
  play: async ({ canvasElement }) => {
    const trigger = await within(canvasElement).findByRole("combobox", { name: "Priority" });
    await expect(window.matchMedia("(width <= 720px)").matches).toBe(true);
    await expect(trigger.closest(".dropdown__trigger")!.getBoundingClientRect().height).toBeGreaterThanOrEqual(44);
    await userEvent.click(trigger);
    const list = within(document.body).getByRole("listbox");
    const box = list.getBoundingClientRect();
    await expect(box.left).toBeGreaterThanOrEqual(0);
    await expect(box.top).toBeGreaterThanOrEqual(0);
    await expect(box.right).toBeLessThanOrEqual(window.innerWidth);
    await expect(box.bottom).toBeLessThanOrEqual(window.innerHeight);
    for (const option of within(list).getAllByRole("option")) await expect(option).toBeVisible();
  },
};

// Each state's share of the time, the one a person is waiting on drawn apart.
export const StateShares: Story = {
  args: { initial: { days: 30 } },
  beforeEach: () =>
    stubApi({
      getReport: async () =>
        makeReport({
          states: [
            makeReportState("open", { stays: 4, duration: { p50_seconds: 3600, p90_seconds: 7200 }, share: 0.6 }),
            makeReportState("discussion", { stays: 2, ongoing: 1, duration: { p50_seconds: 1800, p90_seconds: 3600 }, share: 0.4 }),
          ],
        }),
      getLlmUsageReport: async () => makeLlmUsageReport(),
      getScopeCostReport: async () => makeScopeCostReport(),
      getShadowDecisionsReport: async () => makeShadowDecisionsReport(),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("60% of time")).toBeVisible();
    await expect(canvas.getByText("40% of time")).toBeVisible();
  },
};
