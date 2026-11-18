import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ReplaceAgentChoice } from "./replace_agent_choice";
import "./authorize_page.scss";

const AGENTS = [
  { id: "a1", name: "builder-1" },
  { id: "a2", name: "reviewer-2" },
];

function Choice({ agents }: { agents: typeof AGENTS }) {
  const [replacing, setReplacing] = useState(false);
  const [replaceId, setReplaceId] = useState("a2");
  return (
    <ReplaceAgentChoice
      name="replace"
      agents={agents}
      replacing={replacing}
      replaceId={replaceId}
      onReplacing={setReplacing}
      onReplaceId={setReplaceId}
    />
  );
}

const meta = {
  title: "Auth/ReplaceAgentChoice",
  component: Choice,
  args: { agents: AGENTS },
} satisfies Meta<typeof Choice>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NotReplacing: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("checkbox", { name: "Replace an existing agent" })).not.toBeChecked();
    await expect(canvas.queryByText(/disabled and archived/)).toBeNull();
  },
};

export const Replacing: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("checkbox", { name: "Replace an existing agent" }));
    await expect(canvas.getByText(/disabled and archived/)).toBeVisible();
    await expect(canvas.getByText("reviewer-2", { selector: "strong" })).toBeVisible();
  },
};

export const NothingToReplace: Story = {
  args: { agents: [] },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("checkbox", { name: "Replace an existing agent" })).toBeDisabled();
  },
};
