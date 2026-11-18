import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { DEFAULT_SLOT_CAP } from "@/domain/slot_cap";
import { AgentKindChoice, type NewAgentKind } from "./agent_kind_choice";
import "./authorize_page.scss";

function Choice() {
  const [kind, setKind] = useState<NewAgentKind>("worker");
  const [slotCap, setSlotCap] = useState(String(DEFAULT_SLOT_CAP));
  return <AgentKindChoice name="kind" kind={kind} onChange={setKind} slotCap={slotCap} onSlotCap={setSlotCap} />;
}

const meta = {
  title: "Auth/AgentKindChoice",
  component: Choice,
} satisfies Meta<typeof Choice>;

export default meta;
type Story = StoryObj<typeof meta>;

export const WorkerByDefault: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("radio", { name: "A worker" })).toBeChecked();
    await expect(canvas.getByText(/takes work from the queue/)).toBeVisible();
    await expect(canvas.queryByText(/never waits in the queue/)).toBeNull();
  },
};

export const Console: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("radio", { name: "My interactive session (console)" }));
    await expect(canvas.getByText(/owner-only actions/)).toBeVisible();
    await expect(canvas.queryByText(/takes work from the queue/)).toBeNull();
  },
};

export const Supervisor: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.queryByLabelText("Workers it may run at once")).toBeNull();
    await userEvent.click(canvas.getByRole("radio", { name: "A supervisor" }));
    await expect(canvas.getByText(/without asking you again/)).toBeVisible();
    await expect(canvas.getByLabelText("Workers it may run at once")).toHaveValue(4);
  },
};
