import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect } from "storybook/test";
import { apiClient } from "@/api";
import SupervisorsPage from "./supervisors_page";

const meta = {
  title: "Workers/SupervisorsPage",
  component: SupervisorsPage,
  parameters: { layout: "padded" },
} satisfies Meta<typeof SupervisorsPage>;

export default meta;
type Story = StoryObj<typeof meta>;

export const SetupOnly: Story = {
  beforeEach: () => {
    apiClient.getSetting = async () => ({ name: "provision_from_board", type: "bool", bool_val: false, is_default: true }) as never;
    apiClient.getSupervisorStatus = async () => ({ offline: false, claimable: 0, last_polled_at: null, slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 });
  },
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole("link", { name: "Download setup script" })).toBeVisible();
    await expect(canvas.queryByRole("link", { name: "Workers page" })).toBeNull();
  },
};
