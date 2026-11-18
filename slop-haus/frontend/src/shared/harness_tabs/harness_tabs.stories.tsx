import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import HarnessTabs from "./harness_tabs";

const meta = {
  title: "Harness/HarnessTabs",
  component: HarnessTabs,
  args: { children: (h) => <p>Instructions for {h.label}, run as {h.binary}.</p> },
} satisfies Meta<typeof HarnessTabs>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("tab", { name: "Claude Code" })).toHaveAttribute("aria-selected", "true");
  },
};

export const ArrowKeys: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    canvas.getByRole("tab", { name: "Claude Code" }).focus();
    await userEvent.keyboard("{ArrowRight}");
    await expect(canvas.getByRole("tab", { name: "pi" })).toHaveAttribute("aria-selected", "true");
  },
};
