import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import ProviderButtons, { OrDivider } from "./provider_buttons";

const meta: Meta<typeof ProviderButtons> = {
  title: "ProviderButtons",
  component: ProviderButtons,
  args: {
    providers: [
      { name: "dex", displayName: "Dex" },
      { name: "github", displayName: "GitHub" },
    ],
    label: (name: string) => `Continue with ${name}`,
    onChoose: fn(),
  },
};

export default meta;
type Story = StoryObj<typeof ProviderButtons>;

export const Two: Story = {};

export const WithDivider: Story = {
  render: (args) => (
    <div>
      <ProviderButtons {...args} />
      <OrDivider label="or" />
    </div>
  ),
};

// Starting one disables them all, so a second click can't start a second
// round trip over the first.
export const DisabledWhileStarting: Story = {
  args: { onChoose: fn(() => new Promise<void>(() => {})) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Continue with Dex" }));
    expect(canvas.getByRole("button", { name: "Continue with GitHub" })).toBeDisabled();
  },
};
