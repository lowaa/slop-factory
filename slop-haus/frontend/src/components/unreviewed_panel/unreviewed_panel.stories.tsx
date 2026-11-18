import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { stubApi } from "@/test/story_harness";
import { makeTicket } from "@/test/fixtures";
import UnreviewedPanel from "./unreviewed_panel";
import { DEFAULT_UNREVIEWED_VALUES, UNREVIEWED_FILTER_MODEL } from "@/domain/unreviewed_filter";

const tickets = [
  makeTicket({ id: 7, title: "Merged without approval", status: "done", closed_at: "2026-10-02T09:00:00Z", pr_url: "https://github.com/acme/repo/pull/7" }),
  makeTicket({ id: 8, title: "Merged, no PR reported", status: "done", closed_at: "2026-10-01T09:00:00Z", pr_url: "" }),
];

const meta = {
  title: "Panels/UnreviewedPanel",
  component: UnreviewedPanel,
  args: { values: DEFAULT_UNREVIEWED_VALUES, onFilterChange: () => {}, sort: "recently_closed", onSortChange: () => {} },
} satisfies Meta<typeof UnreviewedPanel>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Listed: Story = {
  beforeEach: () =>
    stubApi({
      listTicketsPage: async ({ cursor }) =>
        cursor
          ? { tickets: [makeTicket({ id: 6, title: "Merged earlier", status: "done", closed_at: "2026-09-30T09:00:00Z" })] }
          : { tickets, nextCursor: "c2" },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("link", { name: /Merged without approval/ })).toBeVisible();
    await expect(canvas.getByText("no PR reported")).toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: "Load more" }));
    await expect(await canvas.findByRole("link", { name: /Merged earlier/ })).toBeVisible();
  },
};

export const Filtered: Story = {
  args: { values: UNREVIEWED_FILTER_MODEL.fromSearch({ q: "auth", from: "2026-10-01", to: "2026-10-05" }) },
  beforeEach: () => stubApi({ listTicketsPage: async () => ({ tickets: [] }) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("No tickets match these filters.")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Clear filters" })).toBeVisible();
  },
};
