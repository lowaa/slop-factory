import type { Meta, StoryObj } from "@storybook/react-vite";
import Command from "./command";

const meta = {
  title: "Widgets/Command",
  component: Command,
  parameters: { layout: "padded" },
} satisfies Meta<typeof Command>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  args: { value: ".slop-haus/run_remote_control.sh --harness pi", label: "the start command" },
};

export const Multiline: Story = {
  args: { value: "mkdir -p ~/agents/builder-1\ncd ~/agents/builder-1", label: "the directory commands" },
};
