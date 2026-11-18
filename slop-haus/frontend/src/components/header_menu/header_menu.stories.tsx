import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { Link } from "@slop-factory/router";
import HeaderMenu from "./header_menu";

// The phone header's menu, with the kind of content app_shell puts in it.
const meta = {
  title: "Widgets/HeaderMenu",
  component: HeaderMenu,
  parameters: { layout: "centered" },
  args: {
    children: (
      <>
        <Link to="/workers">Workers</Link>
        <Link to="/">Board</Link>
      </>
    ),
  },
} satisfies Meta<typeof HeaderMenu>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Closed: Story = {
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("button", { name: "Menu" })).toHaveAttribute("aria-expanded", "false");
  },
};

export const Open: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const button = canvas.getByRole("button", { name: "Menu" });
    await expect(button).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(button);
    await expect(await canvas.findByRole("link", { name: "Workers" })).toBeVisible();
  },
};
