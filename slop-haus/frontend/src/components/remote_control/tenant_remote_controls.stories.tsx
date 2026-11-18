import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { makeConductor, makeConductorDirectoryEntry, makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import TenantRemoteControls from "./tenant_remote_controls";

const OFF = "2026-01-02T00:00:00Z";
const mine = makeConductor({ id: "c-mine", name: "my-laptop" });
const entries = [
  makeConductorDirectoryEntry({ id: "c-mine", name: "my-laptop", owner_display_name: "Ada", has_live_session: true }),
  makeConductorDirectoryEntry({ id: "c-olive", name: "olive-box", owner_display_name: "Olive", host_seen_at: null }),
  makeConductorDirectoryEntry({ id: "c-off", name: "old-box", owner_display_name: "Olive", disabled_at: OFF }),
  makeConductorDirectoryEntry({ id: "c-gone", name: "gone-box", owner_display_name: "Olive", disabled_at: OFF, archived_at: OFF }),
];

const stubs = (isOwner: boolean) => () =>
  stubApi({
    listGrants: async () => [],
    conductorDirectory: async () => entries,
    listConductors: async () => ({ own: [mine], others: [] }),
    listTenants: async () => [makeTenant({ is_owner: isOwner })],
  });

const meta = {
  title: "RemoteControl/TenantRemoteControls",
  component: TenantRemoteControls,
  args: { asView: true },
  parameters: { layout: "padded" },
  beforeEach: stubs(false),
} satisfies Meta<typeof TenantRemoteControls>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Member: Story = {
  play: async ({ canvas }) => {
    const theirs = (await canvas.findByText("olive-box")).closest("li") as HTMLElement;
    await expect(within(theirs).queryByRole("button")).not.toBeInTheDocument();
    const own = (await canvas.findByText("my-laptop")).closest("li") as HTMLElement;
    await expect(within(own).getByRole("button", { name: "Disable" })).toBeVisible();
    await expect(within(own).getByText("Live session")).toBeVisible();
  },
};

export const TenantOwner: Story = {
  beforeEach: stubs(true),
  play: async ({ canvas }) => {
    const theirs = (await canvas.findByText("olive-box")).closest("li") as HTMLElement;
    await expect(await within(theirs).findByRole("button", { name: "Disable" })).toBeVisible();
  },
};

export const ShowRemoved: Story = {
  play: async ({ canvas }) => {
    await userEvent.click(await canvas.findByRole("checkbox", { name: "Show removed (1)" }));
    await expect(await canvas.findByText("gone-box")).toBeVisible();
  },
};

export const Empty: Story = {
  beforeEach: () => stubApi({ listGrants: async () => [], conductorDirectory: async () => [], listConductors: async () => ({ own: [], others: [] }), listTenants: async () => [makeTenant()] }),
  play: async ({ canvas }) => {
    await expect(await canvas.findByText("No one in this tenant has a remote control yet.")).toBeVisible();
  },
};
