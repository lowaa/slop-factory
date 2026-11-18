import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ApiError } from "@/api";
import { makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import ArchiveTenantSection from "./archive_tenant_section";
import "./tenant_settings_page.scss";

const meta = {
  title: "Settings/ArchiveTenantSection",
  component: ArchiveTenantSection,
  beforeEach: () => stubApi({ listTenants: async () => [makeTenant({ is_owner: true })] }),
} satisfies Meta<typeof ArchiveTenantSection>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NameMustBeTyped: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: /Archive this tenant/ }));
    const archive = canvas.getByRole("button", { name: "Archive tenant" });
    await expect(archive).toBeDisabled();
    await userEvent.type(canvas.getByLabelText("Type the tenant's name to confirm"), makeTenant().name);
    await expect(archive).toBeEnabled();
  },
};

export const LastActiveTenant: Story = {
  beforeEach: () =>
    stubApi({
      listTenants: async () => [makeTenant({ is_owner: true })],
      archiveTenant: async () => {
        throw new ApiError(409, "this is your only active tenant; create another first", "last_active_tenant");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: /Archive this tenant/ }));
    await userEvent.type(canvas.getByLabelText("Type the tenant's name to confirm"), makeTenant().name);
    await userEvent.click(canvas.getByRole("button", { name: "Archive tenant" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("only active tenant");
  },
};

export const NotAnOwner: Story = {
  beforeEach: () => stubApi({ listTenants: async () => [makeTenant({ is_owner: false })] }),
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).queryByRole("button", { name: /Archive this tenant/ })).toBeNull();
  },
};
