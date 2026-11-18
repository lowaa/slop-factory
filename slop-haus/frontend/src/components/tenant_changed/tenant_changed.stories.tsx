import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import TenantChanged from "./tenant_changed";

const was = makeTenant({ id: "11111111-1111-1111-1111-111111111111", name: "Ada's workspace", current: false });
const now = makeTenant({ id: "22222222-2222-2222-2222-222222222222", name: "Grace's workspace", current: true });

const meta = {
  title: "Components/TenantChanged",
  component: TenantChanged,
  args: { was: was.id },
  beforeEach: () => stubApi({ listTenants: async () => [was, now] }),
} satisfies Meta<typeof TenantChanged>;

export default meta;
type Story = StoryObj<typeof meta>;

export const SwitchedInAnotherTab: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Reload into Grace's workspace" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Switch back to Ada's workspace" })).toBeEnabled();
  },
};
