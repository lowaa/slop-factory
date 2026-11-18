import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect } from "storybook/test";
import { HelpPage } from "./help_routes";

const meta = {
  title: "Help/HelpPage",
  component: HelpPage,
  parameters: { layout: "padded" },
  args: { slug: "supervisor" },
} satisfies Meta<typeof HelpPage>;

export default meta;
type Story = StoryObj<typeof meta>;

export const SupervisorGuide: Story = {
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole("heading", { name: /Running workers/ })).toBeVisible();
  },
};

export const UnknownPage: Story = {
  args: { slug: "nope" },
  play: async ({ canvas }) => {
    await expect(await canvas.findByText(/no help page called/)).toBeVisible();
  },
};
