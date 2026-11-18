import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ApiError } from "@/api";
import { makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import { chooseOption } from "../../test/choose_option";
import TenantSwitcher from "./tenant_switcher";

const meta = {
  title: "Widgets/TenantSwitcher",
  component: TenantSwitcher,
  beforeEach: () =>
    stubApi({
      listTenants: async () => [makeTenant({ id: "t1", name: "Ada's workspace" }), makeTenant({ id: "t2", name: "Lab", current: false })],
      listArchivedTenants: async () => [],
    }),
} satisfies Meta<typeof TenantSwitcher>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("combobox", { name: "Tenant" })).toHaveTextContent("Ada's workspace");
  },
};

export const SwitchRefused: Story = {
  beforeEach: () =>
    stubApi({
      listTenants: async () => [makeTenant({ id: "t1", name: "Ada's workspace" }), makeTenant({ id: "t2", name: "Lab", current: false })],
      listArchivedTenants: async () => [],
      switchTenant: async () => {
        throw new ApiError(403, "not a member of that tenant");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("combobox", { name: "Tenant" });
    await chooseOption(canvasElement, "Tenant", "Lab");
    await expect(await canvas.findByRole("alert")).toHaveTextContent("not a member of that tenant");
  },
};

export const SeveralTenantsSharingAnOwner: Story = {
  beforeEach: () =>
    stubApi({
      listArchivedTenants: async () => [],
      listTenants: async () => [
        makeTenant({ id: "t1", name: "Acme Web", colour: "violet", owners: ["Alex"] }),
        makeTenant({ id: "t2", name: "Acme Web Test", current: false, colour: "teal", owners: ["Alex"] }),
        makeTenant({ id: "t3", name: "Lab", current: false, owners: ["Bea", "Cy"] }),
      ],
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = await canvas.findByRole("combobox", { name: "Tenant" });
    await expect(trigger.querySelector("[data-colour=violet]")).not.toBeNull();
    await userEvent.click(trigger);
    // The popup is portalled-free but may sit outside the canvas root's queries.
    const body = within(canvasElement.ownerDocument.body);
    await expect(await body.findByRole("option", { name: /^Acme Web\s*owner: Alex$/ })).toBeVisible();
    await expect(await body.findByRole("option", { name: /^Acme Web Test\s*owner: Alex$/ })).toBeVisible();
    await expect(await body.findByRole("option", { name: /^Lab\s*owners: Bea, Cy$/ })).toBeVisible();
  },
};

export const ShowArchived: Story = {
  beforeEach: () =>
    stubApi({
      listTenants: async () => [makeTenant({ id: "t1", name: "Ada's workspace" }), makeTenant({ id: "t2", name: "Lab", current: false })],
      listArchivedTenants: async () => [makeTenant({ id: "t9", name: "Old lab", current: false, archived_at: "2026-10-01T00:00:00Z" })],
      unarchiveTenant: async () => {
        throw new ApiError(403, "you already own the most active tenants allowed", "tenant_limit_reached");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Show archived (1)" }));
    await expect(within(canvas.getByRole("group", { name: "Archived tenants" })).getByText("Old lab")).toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: "Unarchive Old lab" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("most active tenants allowed");
  },
};
