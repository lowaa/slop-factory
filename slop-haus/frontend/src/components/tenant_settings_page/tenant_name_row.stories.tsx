import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ApiError } from "@/api";
import { makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import TenantNameRow from "./tenant_name_row";
import "./tenant_settings_page.scss";

const meta = {
  title: "Settings/TenantNameRow",
  component: TenantNameRow,
  decorators: [(Story) => <div className="tenant-settings"><Story /></div>],
} satisfies Meta<typeof TenantNameRow>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Owner: Story = {
  // The list answers with the new name after a rename, as the server does.
  beforeEach: () => {
    let tenant = makeTenant({ name: "Ada's workspace", is_owner: true });
    return stubApi({
      listTenants: async () => [tenant],
      renameTenant: async (name: string) => (tenant = makeTenant({ name, is_owner: true })),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const field = await canvas.findByLabelText("Name");
    await userEvent.type(field, " 2");
    await userEvent.click(canvas.getByRole("button", { name: "Rename" }));
    await expect(await canvas.findByRole("button", { name: "Rename" })).toBeDisabled();
  },
};

export const Refused: Story = {
  beforeEach: () =>
    stubApi({
      listTenants: async () => [makeTenant({ is_owner: true })],
      renameTenant: async () => {
        throw new ApiError(409, "you already belong to a workspace with that name");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(await canvas.findByLabelText("Name"), "x");
    await userEvent.click(canvas.getByRole("button", { name: "Rename" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("already belong");
  },
};

export const Member: Story = {
  beforeEach: () => stubApi({ listTenants: async () => [makeTenant({ name: "Ada's workspace", is_owner: false })] }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("Ada's workspace")).toBeVisible();
    await expect(canvas.queryByLabelText("Name")).toBeNull();
  },
};
