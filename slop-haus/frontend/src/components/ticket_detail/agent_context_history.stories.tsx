import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { makeAgentContext } from "@/test/fixtures";
import AgentContextHistory from "./agent_context_history";

const HANDOFFS = [
  makeAgentContext({
    id: 1,
    created_at: "2026-03-01T09:00:00Z",
    body: "Branch ticket-9-x, tests pass.\nNext: wire up the route.",
  }),
  makeAgentContext({
    id: 2,
    created_at: "2026-03-02T15:30:00Z",
    body: "Route wired, but the URL shape is undecided.\nSee slop-haus/server/internal/ticket/some_really_long_file_name_without_breaks_handler.go",
  }),
  makeAgentContext({
    id: 3,
    created_at: "2026-03-03T11:15:00Z",
    body: "Waiting on the human for the URL shape.\nBranch is pushed and the PR is open as a draft.",
  }),
];

const meta = {
  title: "Ticket detail/AgentContextHistory",
  component: AgentContextHistory,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 640 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof AgentContextHistory>;

export default meta;
type Story = StoryObj<typeof meta>;

function entries(canvasElement: HTMLElement): HTMLDetailsElement[] {
  return Array.from(canvasElement.querySelectorAll("details"));
}

export const NoHandoffs: Story = {
  args: { contexts: [] },
  play: async ({ canvasElement }) => {
    await expect(
      within(canvasElement).getByText(
        "No agent has left a handoff on this ticket yet.",
      ),
    ).toBeInTheDocument();
  },
};

export const SeveralFolded: Story = {
  args: { contexts: HANDOFFS },
  play: async ({ canvasElement }) => {
    // Newest first, and only the newest open.
    await expect(entries(canvasElement).map((d) => d.open)).toEqual([
      true,
      false,
      false,
    ]);
  },
};

export const AllExpanded: Story = {
  args: { contexts: HANDOFFS },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Expand all" }));
    await expect(
      canvas.getByRole("button", { name: "Collapse all" }),
    ).toBeInTheDocument();
    await expect(entries(canvasElement).map((d) => d.open)).toEqual([
      true,
      true,
      true,
    ]);
  },
};
