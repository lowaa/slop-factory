import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn } from "storybook/test";
import { stubApi } from "@/test/story_harness";
import SlotCapControl from "./slot_cap_control";
import "./provisioning.scss";

const supervisor = { id: "s1", name: "box", archived_at: null, created_at: "2026-10-01T00:00:00Z", disabled_at: null, slot_cap: 4, slot_count: 2, last_seen_at: "2026-11-08T00:00:00Z", stale: false };

const meta = {
  title: "Workers/SlotCapControl",
  component: SlotCapControl,
  parameters: { layout: "padded" },
  args: { supervisor, canEdit: true, onChanged: fn(async () => {}) },
  beforeEach: () => stubApi({ setSupervisorSlotCap: async () => ({ ...supervisor, slot_cap: 6 }) }),
} satisfies Meta<typeof SlotCapControl>;

export default meta;
type Story = StoryObj<typeof meta>;

export const RaiseTheCap: Story = {
  play: async ({ canvas, userEvent }) => {
    await expect(canvas.getByText("2 of 4 workers")).toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: "Change cap for box" }));
    const input = canvas.getByLabelText("Workers box may run at once");
    await userEvent.clear(input);
    await userEvent.type(input, "6");
    await expect(canvas.getByRole("button", { name: "Save cap" })).toBeEnabled();
  },
};

export const OutOfRange: Story = {
  play: async ({ canvas, userEvent }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Change cap for box" }));
    const input = canvas.getByLabelText("Workers box may run at once");
    await userEvent.clear(input);
    await userEvent.type(input, "17");
    await expect(canvas.getByRole("button", { name: "Save cap" })).toBeDisabled();
    await expect(canvas.getByRole("note")).toBeVisible();
  },
};

export const OverCap: Story = {
  args: { supervisor: { ...supervisor, slot_cap: 2, slot_count: 4 } },
  play: async ({ canvas }) => {
    await expect(canvas.getByText("4 of 2 workers")).toBeVisible();
    await expect(canvas.getByRole("note")).toBeVisible();
  },
};

export const NotTheOwner: Story = {
  args: { canEdit: false },
  play: async ({ canvas }) => {
    await expect(canvas.getByText("2 of 4 workers")).toBeVisible();
    await expect(canvas.queryByRole("button", { name: /Change cap/ })).toBeNull();
  },
};
