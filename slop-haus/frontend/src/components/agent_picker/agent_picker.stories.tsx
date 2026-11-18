import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { AgentPicker } from "./agent_picker";
import type { PickableAgent } from "./pickable_agents";
import { chooseOption } from "../../test/choose_option";
import { expectPhoneDropdowns } from "../../test/phone_dropdown_checks";
import { useAgentChoice } from "./use_agent_choice";

const AGENTS: PickableAgent[] = [
  { id: "agent-1", name: "builder-1" },
  { id: "agent-2", name: "reviewer-1" },
];

// The sign-in pages own the choice with this hook; using it here keeps the
// radios reacting to clicks, which a fixed stub state would not.
function Picker({ agents }: { agents: PickableAgent[] }) {
  const state = useAgentChoice();
  return <AgentPicker name="agent" agents={agents} state={state} />;
}

const meta = {
  title: "Auth/AgentPicker",
  component: Picker,
  args: { agents: AGENTS },
} satisfies Meta<typeof Picker>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NewAgent: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("radio", { name: "A new agent" })).toBeChecked();
    await expect(canvas.getByRole("textbox", { name: "New agent name" })).toBeVisible();
    await expect(canvas.queryByRole("combobox", { name: "Existing agent" })).toBeNull();
    // Typed text must live in the hook's state: it survives a round trip
    // through the other choice, which unmounts the field.
    await userEvent.type(canvas.getByRole("textbox", { name: "New agent name" }), "scout-1");
    await userEvent.click(canvas.getByRole("radio", { name: "An existing agent" }));
    await userEvent.click(canvas.getByRole("radio", { name: "A new agent" }));
    await expect(canvas.getByRole("textbox", { name: "New agent name" })).toHaveValue("scout-1");
  },
};

export const ExistingAgent: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("radio", { name: "An existing agent" }));
    await expect(canvas.getByRole("radio", { name: "An existing agent" })).toBeChecked();
    await expect(canvas.getByRole("combobox", { name: "Existing agent" })).toHaveTextContent("Choose an agent…");
    await chooseOption(canvasElement, "Existing agent", "reviewer-1");
    await expect(canvas.getByRole("combobox", { name: "Existing agent" })).toHaveTextContent("reviewer-1");
    await expect(canvas.queryByRole("textbox", { name: "New agent name" })).toBeNull();
    // The pick must live in the hook's state, not only the dropdown.
    await userEvent.click(canvas.getByRole("radio", { name: "A new agent" }));
    await userEvent.click(canvas.getByRole("radio", { name: "An existing agent" }));
    await expect(canvas.getByRole("combobox", { name: "Existing agent" })).toHaveTextContent("reviewer-1");
  },
};

export const NoAgentsToPick: Story = {
  args: { agents: [] },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("radio", { name: "An existing agent" })).toBeDisabled();
    await expect(canvas.getByRole("radio", { name: "A new agent" })).toBeChecked();
    await expect(canvas.getByRole("textbox", { name: "New agent name" })).toBeVisible();
  },
};

export const Phone: Story = {
  globals: { viewport: { value: "mobile1", isRotated: false } },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("radio", { name: "An existing agent" }));
    await expectPhoneDropdowns(canvasElement, 1);
  },
};
