import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { makeStatusChange } from "@/test/fixtures";
import StatusHistory from "./status_history";
import "./ticket_detail.scss";

const AGENT = "aaaaaaaa-1111-1111-1111-111111111111";
const USER = "bbbbbbbb-2222-2222-2222-222222222222";
const PROPOSAL = "cccccccc-3333-3333-3333-333333333333";

// The app has one dark theme (index.scss), which the preview loads, so there
// is no second theme to show these in.
const meta = {
  title: "Ticket detail/StatusHistory",
  component: StatusHistory,
} satisfies Meta<typeof StatusHistory>;

export default meta;
type Story = StoryObj<typeof meta>;

export const EveryKindOfCause: Story = {
  args: {
    changes: [
      makeStatusChange({ id: 1, from_status: null, to_status: "open", actor_kind: "user", actor_id: USER, cause: { kind: "other", text: "created" } }),
      makeStatusChange({ id: 2, from_status: "open", to_status: "in_progress", actor_id: AGENT, cause: { kind: "other", text: "claimed" } }),
      makeStatusChange({ id: 3, from_status: "in_progress", to_status: "open", actor_kind: "system", actor_id: null, cause: { kind: "other", text: "lease_expired" } }),
      makeStatusChange({ id: 4, from_status: "open", to_status: "parked", actor_kind: "user", actor_id: USER, cause: { kind: "proposal", proposal_id: PROPOSAL } }),
      makeStatusChange({ id: 5, from_status: "parked", to_status: "preparing", actor_kind: "user", actor_id: USER, cause: { kind: "proposal", proposal_id: PROPOSAL, auto_approved: true } }),
      makeStatusChange({ id: 6, from_status: "preparing", to_status: "parked", actor_kind: "user", actor_id: null, actor_id_deleted: true }),
    ],
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("auto-approved")).toBeVisible();
    await expect(canvas.getByText("System")).toBeVisible();
    await expect(canvas.getByText("Deleted user")).toBeVisible();
    await expect(canvas.getByText("lease_expired")).toBeVisible();
  },
};
