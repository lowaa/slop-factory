import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import SettingNumberRow from "./setting_number_row";
import "./tenant_settings_page.scss";

const meta = {
  title: "Settings/SettingNumberRow",
  component: SettingNumberRow,
  decorators: [(Story) => <div className="tenant-settings"><Story /></div>],
  args: {
    label: "Merge queue: changes landed per CI run",
    description: "How many queued changes the server lands together with one CI run.",
    value: 1,
    min: 1,
    max: 3,
    onWrite: fn(async () => {}),
  },
} satisfies Meta<typeof SettingNumberRow>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Editing: Story = {
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const field = canvas.getByRole("spinbutton");
    await userEvent.clear(field);
    await userEvent.type(field, "3");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(args.onWrite).toHaveBeenCalledWith(3);
  },
};

export const OutOfRange: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const field = canvas.getByRole("spinbutton");
    await userEvent.clear(field);
    await userEvent.type(field, "9");
    await expect(canvas.getByRole("alert")).toHaveTextContent("from 1 to 3");
  },
};

export const ReadOnly: Story = {
  args: { readOnlyNote: "Only an owner of the tenant can change this." },
};
