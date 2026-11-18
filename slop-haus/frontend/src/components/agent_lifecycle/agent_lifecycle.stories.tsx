import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ApiError, type Agent } from "@/api";
import { makeAgent } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import { AgentLifecycleActions, AgentLifecycleFeedback, AgentStateBadge, useAgentLifecycle } from ".";

const OFF = "2026-01-02T00:00:00Z";

function Lifecycle({ agent: initial }: { agent: Agent }) {
  const [agent, setAgent] = useState(initial);
  const lifecycle = useAgentLifecycle(agent, setAgent);
  return (
    <div style={{ display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap" }}>
      <AgentStateBadge agent={agent} />
      <AgentLifecycleActions lifecycle={lifecycle} />
      <AgentLifecycleFeedback lifecycle={lifecycle} />
    </div>
  );
}

const meta = {
  title: "Workers/AgentLifecycle",
  component: Lifecycle,
  parameters: { layout: "padded" },
} satisfies Meta<typeof Lifecycle>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Enabled: Story = {
  args: { agent: makeAgent({ name: "alpha" }) },
  play: async ({ canvas }) => {
    await expect(canvas.getByText("Enabled")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Disable" })).toBeVisible();
  },
};

export const Disabled: Story = {
  args: { agent: makeAgent({ name: "alpha", disabled_at: OFF }) },
  play: async ({ canvas }) => {
    await expect(canvas.getByRole("button", { name: "Enable" })).toBeVisible();
  },
};

export const Removed: Story = {
  args: { agent: makeAgent({ name: "alpha", disabled_at: OFF, archived_at: OFF }) },
  play: async ({ canvas }) => {
    await expect(canvas.getByText("Removed")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Restore" })).toBeVisible();
  },
};

export const ConfirmingRemove: Story = {
  args: { agent: makeAgent({ name: "alpha" }) },
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Remove" }));
    await expect(canvas.getByRole("group", { name: "Confirm removing alpha" })).toBeVisible();
  },
};

export const ArchiveFailsAfterDisable: Story = {
  args: { agent: makeAgent({ id: "a1", name: "alpha" }) },
  beforeEach: () =>
    stubApi({
      setAgentDisabled: async () => makeAgent({ id: "a1", name: "alpha", disabled_at: OFF }),
      archiveAgent: async () => {
        throw new ApiError(500, "boom");
      },
    }),
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Remove" }));
    await userEvent.click(within(canvas.getByRole("group")).getByRole("button", { name: "Remove" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("boom");
    await expect(canvas.getByRole("button", { name: "Retry remove" })).toBeVisible();
  },
};
