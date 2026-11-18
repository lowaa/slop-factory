import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn } from "storybook/test";
import SessionSwitchDialog from "./session_switch_dialog";

// The dialog calls no API, so it needs none of the story harness's stubs.
// The phone stories set the viewport itself, not just the frame's width:
// the stretched actions and touch-sized targets come from a media query on
// the viewport (layout.narrow), which a narrow container would not trigger.
const meta = {
  title: "Remote control/SessionSwitchDialog",
  component: SessionSwitchDialog,
  args: { replying: null, explain: true, onAnswer: fn() },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 640, padding: 12 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof SessionSwitchDialog>;

export default meta;
type Story = StoryObj<typeof meta>;

// Fails if the viewport did not take, which would leave the "phone"
// stories silently checking the desktop layout.
const phone = {
  globals: { viewport: { value: "mobile1", isRotated: false } },
  play: async ({ canvasElement }) => {
    await expect(window.matchMedia("(width <= 720px)").matches).toBe(true);
    const buttons = canvasElement.querySelectorAll(
      ".session-switch__actions button",
    );
    await expect(buttons).toHaveLength(2);
    for (const button of buttons) {
      await expect(
        button.getBoundingClientRect().height,
      ).toBeGreaterThanOrEqual(44);
    }
  },
} satisfies Story;

export const Explanation: Story = {
  play: async ({ canvas }) => {
    await expect(
      canvas.getByRole("checkbox", { name: "Do not show again" }),
    ).toBeVisible();
    await expect(
      canvas.getByRole("button", { name: "Continue" }),
    ).toBeVisible();
  },
};

export const ExplanationPhone: Story = phone;

export const ConfirmWithExplanation: Story = {
  args: { replying: "fix-the-flaky-test" },
};

export const ConfirmWithExplanationPhone: Story = {
  ...phone,
  args: { replying: "fix-the-flaky-test" },
};

// No explanation left to show, so no "Do not show again" either: the
// confirmation guards against losing a reply and cannot be dismissed.
export const ConfirmOnly: Story = {
  play: async ({ canvas }) => {
    await expect(canvas.queryByRole("checkbox")).toBeNull();
    await expect(
      canvas.getByRole("button", { name: "Stop it and continue" }),
    ).toBeVisible();
  },
  args: { replying: "fix-the-flaky-test", explain: false },
};

export const ConfirmOnlyPhone: Story = {
  ...phone,
  args: { replying: "fix-the-flaky-test", explain: false },
};
