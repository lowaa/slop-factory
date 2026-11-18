import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { stubApi } from "@/test/story_harness";
import MergeMethodRow from "./merge_method_row";
import "./tenant_settings_page.scss";

const caps = { merge_method: "worker", merge_queue_enabled: true, change_tools: true, merge_queue_available: true } as const;

const meta = {
  title: "Settings/MergeMethodRow",
  component: MergeMethodRow,
  decorators: [
    (Story) => (
      <div className="tenant-settings">
        <Story />
      </div>
    ),
  ],
  args: { onWrite: fn(async () => {}) },
  beforeEach: () => stubApi({ getLandingCapabilities: async () => ({ ...caps }) }),
} satisfies Meta<typeof MergeMethodRow>;

export default meta;
type Story = StoryObj<typeof meta>;

export const TwoOptionsOnSeparateRows: Story = {
  play: async ({ canvasElement }) => {
    const rows = await within(canvasElement).findAllByTestId("merge-method-choice");
    await expect(rows).toHaveLength(2);
    await expect(rows[0].getBoundingClientRect().bottom).toBeLessThanOrEqual(rows[1].getBoundingClientRect().top);
  },
};

export const ChoosingTheQueue: Story = {
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("radio", { name: /server merges from a queue/ }));
    await expect(args.onWrite).toHaveBeenCalledWith("merge_queue");
  },
};

export const QueueSwitchedOff: Story = {
  beforeEach: () => stubApi({ getLandingCapabilities: async () => ({ ...caps, merge_queue_enabled: false, merge_queue_available: false }) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("radio", { name: /server merges from a queue/ })).toBeDisabled();
    await expect(canvas.getByRole("note")).toHaveTextContent("switched off");
    await expect(canvas.getAllByTestId("merge-method-choice")).toHaveLength(2);
  },
};
