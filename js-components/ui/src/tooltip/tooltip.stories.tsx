import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import Button from "../button/button";
import Tooltip from "./tooltip";

// The bubble only mounts on hover/focus (see tooltip.tsx), which a static
// story can't capture -- open Storybook's own canvas and hover/tab to the
// button to see it.
const meta = {
  title: "Widgets/Tooltip",
  component: Tooltip,
  parameters: { layout: "centered" },
  decorators: [
    (Story) => (
      <div style={{ padding: 48 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof Tooltip>;

export default meta;
type Story = StoryObj<typeof meta>;

const LONG_TEXT = "Saved for you to keep refining -- no agent will pick it up until you open it.";

export const Default: Story = {
  args: {
    text: LONG_TEXT,
    // Play functions hover and expect the bubble at once.
    hoverDelayMs: 0,
    children: <Button>Preparing</Button>,
  },
};

// The margin tooltip.tsx keeps from the screen edge.
const EDGE_MARGIN_PX = 8;

async function expectBubbleOnScreen(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.hover(canvas.getByRole("button"));
  const rect = (await canvas.findByRole("tooltip")).getBoundingClientRect();
  const viewportWidth = canvasElement.ownerDocument.documentElement.clientWidth;
  await expect(rect.left).toBeGreaterThanOrEqual(EDGE_MARGIN_PX);
  await expect(rect.right).toBeLessThanOrEqual(viewportWidth - EDGE_MARGIN_PX);
}

// A short trigger flush against the edge with a wide bubble: centred over
// the trigger, over half the bubble would sit off-screen. This is the
// new-ticket form's left-aligned Preparing button.
export const AtLeftEdge: Story = {
  args: Default.args,
  parameters: { layout: "fullscreen" },
  decorators: [
    (Story) => (
      <div style={{ display: "flex", justifyContent: "flex-start", paddingTop: 120 }}>
        <Story />
      </div>
    ),
  ],
  play: async ({ canvasElement }) => expectBubbleOnScreen(canvasElement),
};

export const AtRightEdge: Story = {
  args: { ...Default.args, children: <Button>Open</Button> },
  parameters: { layout: "fullscreen" },
  decorators: [
    (Story) => (
      <div style={{ display: "flex", justifyContent: "flex-end", paddingTop: 120 }}>
        <Story />
      </div>
    ),
  ],
  play: async ({ canvasElement }) => expectBubbleOnScreen(canvasElement),
};
