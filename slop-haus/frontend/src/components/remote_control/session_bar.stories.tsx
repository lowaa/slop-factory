import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import SessionBar from "./session_bar";

const meta = {
  title: "RemoteControl/SessionBar",
  component: SessionBar,
  args: { title: "my-laptop", onBack: fn(), children: <p>Menu content</p> },
} satisfies Meta<typeof SessionBar>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Closed: Story = {
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await expect(canvas.queryByText("Menu content")).not.toBeInTheDocument();
    await userEvent.click(canvas.getByRole("button", { name: "‹ Sessions" }));
    await expect(args.onBack).toHaveBeenCalledTimes(1);
  },
};

export const MenuOpen: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Session menu" }));
    await expect(canvas.getByText("Menu content")).toBeVisible();
  },
};

export const PhoneLongTitle: Story = {
  globals: { viewport: { value: "mobile1", isRotated: false } },
  args: { title: "a-very-long-machine-name-that-would-not-fit-on-one-line-of-a-phone" },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    // One line: the title gives way, and the menu button stays in view.
    const menu = canvas.getByRole("button", { name: "Session menu" });
    await expect(menu.getBoundingClientRect().right).toBeLessThanOrEqual(window.innerWidth);
  },
};

export const NoMenu: Story = {
  args: { children: undefined },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).queryByRole("button", { name: "Session menu" })).not.toBeInTheDocument();
  },
};
