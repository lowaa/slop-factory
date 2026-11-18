import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect } from "storybook/test";
import SupervisorOfflineBanner from "./supervisor_offline_banner";

const meta = {
  title: "Workers/SupervisorOfflineBanner",
  component: SupervisorOfflineBanner,
  parameters: { layout: "padded" },
} satisfies Meta<typeof SupervisorOfflineBanner>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Offline: Story = {
  args: { status: { offline: true, claimable: 3, last_polled_at: "2026-10-19T10:00:00Z", slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 } },
  play: async ({ canvas }) => {
    await expect(canvas.getByRole("alert")).toHaveTextContent(/Supervisor offline: last polled .*, 3 tickets claimable/);
  },
};

export const OneClaimable: Story = {
  args: { status: { offline: true, claimable: 1, last_polled_at: "2026-10-19T10:00:00Z", slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 } },
  play: async ({ canvas }) => {
    await expect(canvas.getByRole("alert")).toHaveTextContent("1 ticket claimable");
  },
};
