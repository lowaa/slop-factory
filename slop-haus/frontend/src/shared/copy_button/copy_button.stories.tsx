import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent } from "storybook/test";
import CopyButton from "./copy_button";

// The "Select and copy" state appears when neither the Clipboard API nor
// execCommand works, which is a property of the page (a non-secure context, a
// denied permission), not of the props; NothingCanCopy stages it by hiding
// both for the duration of the click.
const meta = {
  title: "Widgets/CopyButton",
  component: CopyButton,
  parameters: { layout: "centered" },
  decorators: [
    (Story) => (
      <div style={{ padding: 48 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof CopyButton>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  args: { value: "go run ./server/cmd/slop setup-workspace -name builder-1", label: "the setup command" },
};

export const Multiline: Story = {
  args: {
    value: "export SLOP_HAUS_ADMIN_TOKEN='...'\nmkdir -p ~/.slop-haus-workspaces/builder-1",
    label: "the setup command",
  },
};

export const Copied: Story = {
  args: { value: "echo hi", label: "the command" },
  play: async ({ canvas }) => {
    // The older path, so the outcome does not depend on the browser's
    // clipboard permission.
    const clipboard = Object.getOwnPropertyDescriptor(Navigator.prototype, "clipboard");
    const execCommand = document.execCommand;
    Object.defineProperty(Navigator.prototype, "clipboard", { configurable: true, get: () => undefined });
    document.execCommand = () => true;
    try {
      await userEvent.click(canvas.getByRole("button", { name: "Copy the command" }));
      await expect(await canvas.findByText("Copied")).toBeVisible();
    } finally {
      if (clipboard) Object.defineProperty(Navigator.prototype, "clipboard", clipboard);
      document.execCommand = execCommand;
    }
  },
};

export const NothingCanCopy: Story = {
  args: { value: "echo hi", label: "the command" },
  play: async ({ canvas }) => {
    const clipboard = Object.getOwnPropertyDescriptor(Navigator.prototype, "clipboard");
    const execCommand = document.execCommand;
    Object.defineProperty(Navigator.prototype, "clipboard", { configurable: true, get: () => undefined });
    document.execCommand = () => false;
    try {
      await userEvent.click(canvas.getByRole("button", { name: "Copy the command" }));
      await expect(await canvas.findByText("Select and copy")).toBeVisible();
    } finally {
      if (clipboard) Object.defineProperty(Navigator.prototype, "clipboard", clipboard);
      document.execCommand = execCommand;
    }
  },
};
