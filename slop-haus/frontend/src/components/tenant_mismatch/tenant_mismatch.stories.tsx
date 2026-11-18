import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { ApiError } from "@/api";
import { makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import TenantMismatch from "./tenant_mismatch";

const current = makeTenant({ id: "11111111-1111-1111-1111-111111111111", name: "Ada's workspace", current: true });
const target = makeTenant({ id: "22222222-2222-2222-2222-222222222222", name: "Grace's workspace", current: false });

const meta = {
  title: "Components/TenantMismatch",
  component: TenantMismatch,
  args: { ticketId: 42, tenantId: target.id, back: <a href="/board">Back to board</a> },
  beforeEach: () => stubApi({ listTenants: async () => [current, target] }),
} satisfies Meta<typeof TenantMismatch>;

export default meta;
type Story = StoryObj<typeof meta>;

export const MismatchPrompt: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Switch to Grace's workspace" })).toBeEnabled();
    await expect(canvas.getByText("Ada's workspace")).toBeInTheDocument();
  },
};

// switchTenant never settles, so the component stays in its switching state
// (and never reaches reloadPage, which would reload the Storybook frame).
export const Switching: Story = {
  beforeEach: (ctx) => {
    ctx.switchTenant = fn(() => new Promise<void>(() => {}));
    stubApi({ switchTenant: ctx.switchTenant });
  },
  play: async ({ canvasElement, switchTenant }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Switch to Grace's workspace" }));
    await expect(switchTenant).toHaveBeenCalledWith(target.id);
    await expect(await canvas.findByRole("button", { name: "Switching…" })).toBeDisabled();
  },
};

export const SwitchFailed: Story = {
  beforeEach: (ctx) => {
    ctx.switchTenant = fn(async () => {
      throw new ApiError(403, "not a member of that tenant");
    });
    stubApi({ switchTenant: ctx.switchTenant });
  },
  play: async ({ canvasElement, switchTenant }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Switch to Grace's workspace" }));
    await expect(switchTenant).toHaveBeenCalledWith(target.id);
    await expect(await canvas.findByRole("alert")).toHaveTextContent("not a member of that tenant");
    // The person can try again.
    await expect(canvas.getByRole("button", { name: "Switch to Grace's workspace" })).toBeEnabled();
  },
};

export const NotAMember: Story = {
  beforeEach: () => stubApi({ listTenants: async () => [current] }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/not a member of/)).toBeInTheDocument();
    await expect(canvas.queryByRole("button")).not.toBeInTheDocument();
  },
};
