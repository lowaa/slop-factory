import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect } from "storybook/test";
import SupervisorSlots from "./supervisor_slots";

const NOW = Date.parse("2026-10-19T10:10:00Z");

const slot = {
  agent_id: "alpha",
  agent_name: "alpha",
  error_class: "",
  error_streak: 0,
  offline: false,
  pause_reason: "",
  reason: "unknown" as const,
  reason_detail: "",
  bundle_hash: "",
  bundle_status: "unknown" as const,
  paused_at: null,
  paused_long: false,
  phase: "running" as const,
  phase_entered_at: "2026-10-19T10:00:00Z",
  reported_at: "2026-10-19T10:09:50Z",
  stall_reason: "" as const,
  stalled: false,
  ticket_id: 42,
};

const meta = {
  title: "Workers/SupervisorSlots",
  component: SupervisorSlots,
  parameters: { layout: "padded" },
} satisfies Meta<typeof SupervisorSlots>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Healthy: Story = {
  args: { now: NOW, slots: [slot] },
  play: async ({ canvas }) => {
    await expect(canvas.getByText("running for 10m")).toBeVisible();
  },
};

export const Stalled: Story = {
  args: {
    now: NOW,
    slots: [
      slot,
      { ...slot, agent_id: "beta", agent_name: "beta", phase: "backoff", ticket_id: null, error_class: "rate_limited", error_streak: 5, stalled: true, stall_reason: "repeating_error" },
    ],
  },
  play: async ({ canvas }) => {
    await expect(canvas.getByRole("alert")).toHaveTextContent("Stalled: stuck repeating the same error");
  },
};

export const Paused: Story = {
  args: {
    now: NOW,
    slots: [{ ...slot, phase: "paused", ticket_id: null, paused_at: "2026-10-19T10:00:00Z", paused_long: true, pause_reason: "git push refused: no credentials" }],
  },
  play: async ({ canvas }) => {
    await expect(canvas.getByRole("alert")).toHaveTextContent("Paused for 10m");
    await expect(canvas.getByRole("alert")).toHaveTextContent("git push refused: no credentials");
  },
};

export const WhyIdle: Story = {
  args: {
    now: NOW,
    slots: [{ ...slot, phase: "idle", ticket_id: null, reason: "lock_queued", reason_detail: "behind #9" }],
  },
  play: async ({ canvas }) => {
    await expect(canvas.getByText(/queued behind another change.*behind #9/)).toBeVisible();
  },
};
