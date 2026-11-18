import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import RichText, { RichTextMode } from "./rich_text";

const meta = {
  title: "Widgets/RichText",
  component: RichText,
  args: { text: "Some **bold** text and a [link](https://example.com).\n\n- one\n- two" },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 480, padding: 16 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof RichText>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Rendered: Story = {
  decorators: [(Story) => <RichTextMode render><Story /></RichTextMode>],
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("link", { name: "link" })).toBeInTheDocument();
  },
};

export const Plain: Story = {
  decorators: [(Story) => <RichTextMode render={false}><Story /></RichTextMode>],
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("button", { name: "Copy" })).toBeInTheDocument();
  },
};
