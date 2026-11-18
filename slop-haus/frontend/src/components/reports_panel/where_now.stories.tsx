import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ApiError, type ReportWaitingTicket } from "@/api";
import { stubApi } from "@/test/story_harness";
import WhereNow, { WAITING_PAGE_SIZE } from "./where_now";

function waiting(count: number, firstId = 1): ReportWaitingTicket[] {
  return Array.from({ length: count }, (_, i) => ({
    id: firstId + i,
    priority: "normal",
    since: "2026-10-01T09:00:00Z",
    title: `Waiting ticket ${firstId + i}`,
  }));
}

const meta = {
  title: "Panels/ReportsPanel/WhereNow",
  component: WhereNow,
  args: {
    rows: [
      { status: "review", count: 3, without_history: 0 },
      { status: "open", count: 25, without_history: 2 },
    ],
  },
} satisfies Meta<typeof WhereNow>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Folded: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    for (const button of canvas.getAllByRole("button")) {
      await expect(button).toHaveAttribute("aria-expanded", "false");
    }
  },
};

export const StatusOpen: Story = {
  beforeEach: () =>
    stubApi({
      // Only review has tickets: a request for another status shows none.
      getReportWaiting: async ({ status }) =>
        status === "review"
          ? { tickets: waiting(3), total: 3 }
          : { tickets: [], total: 0 },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: /Review/ }));
    await expect(
      await canvas.findByText("Showing 3 of 3, longest waiting first."),
    ).toBeVisible();
    await expect(
      canvas.getByRole("link", { name: /Waiting ticket 3\b/ }),
    ).toBeVisible();
    await expect(
      canvas.queryByRole("button", { name: "Show more" }),
    ).toBeNull();
  },
};

export const ShowMore: Story = {
  beforeEach: () =>
    stubApi({
      getReportWaiting: async ({ offset }) => ({
        tickets: waiting(WAITING_PAGE_SIZE, (offset ?? 0) + 1),
        total: WAITING_PAGE_SIZE * 2,
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: /Open/ }));
    await userEvent.click(
      await canvas.findByRole("button", { name: "Show more" }),
    );
    await expect(
      await canvas.findByText(
        `Showing ${WAITING_PAGE_SIZE * 2} of ${WAITING_PAGE_SIZE * 2}, longest waiting first.`,
      ),
    ).toBeVisible();
    await expect(
      canvas.getByRole("link", { name: /Waiting ticket 1\b/ }),
    ).toBeVisible();
    await expect(
      canvas.getByRole("link", { name: /Waiting ticket 11\b/ }),
    ).toBeVisible();
    await expect(
      canvas.queryByRole("button", { name: "Show more" }),
    ).toBeNull();
  },
};

export const TryAgain: Story = {
  beforeEach: () => {
    let calls = 0;
    stubApi({
      getReportWaiting: async () => {
        calls += 1;
        if (calls === 1) throw new ApiError(500, "The report is unavailable.");
        return { tickets: waiting(2), total: 2 };
      },
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: /Review/ }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "The report is unavailable.",
    );
    await userEvent.click(canvas.getByRole("button", { name: "Try again" }));
    await expect(
      await canvas.findByText("Showing 2 of 2, longest waiting first."),
    ).toBeVisible();
    await expect(canvas.queryByRole("alert")).toBeNull();
  },
};
