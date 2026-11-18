import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import TenantArchived from "./tenant_archived";

const meta = {
  title: "Components/TenantArchived",
  component: TenantArchived,
  beforeEach: () => stubApi({ listTenants: async () => [makeTenant({ id: "t2", name: "Lab", current: false })] }),
} satisfies Meta<typeof TenantArchived>;

export default meta;
type Story = StoryObj<typeof meta>;

export const OffersOtherTenants: Story = {
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("button", { name: "Switch to Lab" })).toBeEnabled();
  },
};

export const NoOtherTenant: Story = {
  beforeEach: () => stubApi({ listTenants: async () => [] }),
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByText("You have no other tenant to switch to.")).toBeVisible();
  },
};
