import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import ContextMeter from "./context_meter";

const meta = {
  title: "RemoteControl/ContextMeter",
  component: ContextMeter,
  args: { tokens: 50_000 },
} satisfies Meta<typeof ContextMeter>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Neutral: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("Context: 50k")).toBeVisible();
    await expect(canvas.queryByText(/consider Clear or Compact/)).not.toBeInTheDocument();
  },
};

export const Elevated: Story = { args: { tokens: 120_000 } };

export const LongWithHint: Story = {
  args: { tokens: 214_000 },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByText(/consider Clear or Compact/)).toBeVisible();
  },
};

export const Unknown: Story = {
  args: { tokens: 0 },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).queryByText(/Context:/)).not.toBeInTheDocument();
  },
};
