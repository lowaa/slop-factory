import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import SettingMinutesRow from "./setting_minutes_row";
import "./tenant_settings_page.scss";

const meta = {
  title: "Settings/SettingMinutesRow",
  component: SettingMinutesRow,
  decorators: [(Story) => <div className="tenant-settings"><Story /></div>],
  args: {
    label: "Flag a silent integration-lock holder after",
    description: "Nothing is released automatically.",
    value: 60,
    min: 5,
    max: 1440,
    onWrite: fn(async () => {}),
  },
} satisfies Meta<typeof SettingMinutesRow>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Editing: Story = {
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    const field = canvas.getByRole("spinbutton");
    await userEvent.clear(field);
    await userEvent.type(field, "90");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(args.onWrite).toHaveBeenCalledWith(90);
  },
};
