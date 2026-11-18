import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import TenantColourRow from "./tenant_colour_row";
import "./tenant_settings_page.scss";

const meta = {
  title: "Settings/TenantColourRow",
  component: TenantColourRow,
  decorators: [(Story) => <div className="tenant-settings"><Story /></div>],
} satisfies Meta<typeof TenantColourRow>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Owner: Story = {
  // The list answers with the saved colour afterwards, as the server does.
  beforeEach: () => {
    let tenant = makeTenant({ is_owner: true });
    return stubApi({
      listTenants: async () => [tenant],
      setTenantColour: async (colour) => (tenant = makeTenant({ is_owner: true, colour })),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("radio", { name: "Violet" }));
    await userEvent.click(canvas.getByRole("button", { name: "Save colour" }));
    await expect(await canvas.findByRole("radio", { name: "Violet" })).toBeChecked();
    await expect(canvas.getByRole("button", { name: "Save colour" })).toBeDisabled();
  },
};

export const Member: Story = {
  beforeEach: () => stubApi({ listTenants: async () => [makeTenant({ is_owner: false, colour: "amber" })] }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/only an owner/i)).toBeVisible();
    await expect(canvas.queryByRole("radio")).toBeNull();
  },
};
