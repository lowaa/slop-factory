import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import type { TicketAttempt } from "@/api";
import { makeAttempt, makeReviewFinding, makeReviewRun } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import ReviewRunsSection from "./review_runs_section";
import "./ticket_detail.scss";

type Signals = NonNullable<TicketAttempt["signals"]>;

const meta = {
  title: "Ticket detail/ReviewRunsSection",
  component: ReviewRunsSection,
  parameters: { layout: "padded" },
  args: { ticketId: 7, attempt: makeAttempt({ signals: { head_sha: "abcdef1" } as Signals }) },
} satisfies Meta<typeof ReviewRunsSection>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Empty: Story = {
  beforeEach: () => stubApi({ listAttemptReviewRuns: async () => [] }),
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByText("No review has been posted for this attempt.")).toBeVisible();
  },
};

export const Failed: Story = {
  beforeEach: () =>
    stubApi({
      listAttemptReviewRuns: async () => [
        makeReviewRun({ status: "failed", verdict: "", failure: { class: "usage_limit", message: "you hit your limit" } }),
      ],
    }),
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("alert")).toHaveTextContent("you hit your limit");
  },
};

export const CurrentAndOlderHeads: Story = {
  beforeEach: () =>
    stubApi({
      listAttemptReviewRuns: async () => [
        makeReviewRun({
          id: "1",
          head_sha: "abcdef1111",
          personas: ["security"],
          verdict: "request_changes",
          findings: [makeReviewFinding({ summary: "Leaks a handle", detail: "Never closed." })],
        }),
        makeReviewRun({ id: "2", head_sha: "0123456789", incomplete: true }),
      ],
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("current head")).toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: /Leaks a handle/ }));
    await expect(canvas.getByText("Never closed.")).toBeVisible();
    await expect(canvas.getByText("unlabelled")).toBeVisible();
  },
};

export const LoadError: Story = {
  beforeEach: () =>
    stubApi({
      listAttemptReviewRuns: async () => {
        throw new Error("down");
      },
    }),
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("alert")).toHaveTextContent("Failed to load the review runs.");
  },
};
