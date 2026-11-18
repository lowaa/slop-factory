import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { makeAttempt, makeTicket } from "@/test/fixtures";
import ReplayFollowUp from "./replay_follow_up";
import "./ticket_detail.scss";

const meta = {
  title: "Ticket detail/ReplayFollowUp",
  component: ReplayFollowUp,
  parameters: { layout: "padded" },
  args: {
    ticket: makeTicket({ id: 7, title: "Do the thing", body: "Original spec", status: "done" }),
    attempt: makeAttempt({
      number: 2,
      replay: true,
      shadow: true,
      status: "closed",
      pr_url: "https://example.test/org/repo/pull/12",
    }),
  },
} satisfies Meta<typeof ReplayFollowUp>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Offered: Story = {};

export const Drafting: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Start a follow-up ticket" }));
    await expect(canvas.getByLabelText("Title")).toHaveValue("Follow-up to #7: Do the thing");
  },
};
