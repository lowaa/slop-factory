import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { TestRouterProvider } from "@slop-factory/router/testing/provider";
import { AuthContext } from "@/context/auth";
import { makeProposal } from "@/test/fixtures";
import { signedInAuth } from "@/test/render_signed_in";
import ConversationProposals from "./conversation_proposals";
import "./conversation_view.scss";

const meta = {
  title: "RemoteControl/ConversationProposals",
  component: ConversationProposals,
  decorators: [
    (Story) => (
      <TestRouterProvider>
        <AuthContext.Provider value={signedInAuth()}>
          <Story />
        </AuthContext.Provider>
      </TestRouterProvider>
    ),
  ],
  args: { proposals: [makeProposal({ id: "p1" }), makeProposal({ id: "p2", ticket_id: 8, text: "", stale_reason: "the ticket was edited" })] },
} satisfies Meta<typeof ConversationProposals>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Pending: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("Use Postgres.")).toBeVisible();
    await expect(canvas.getAllByRole("link", { name: /Review and approve/ })).toHaveLength(2);
  },
};

export const PhoneWidth: Story = {
  globals: { viewport: { value: "mobile1", isRotated: false } },
  play: async ({ canvasElement }) => {
    const link = within(canvasElement).getAllByRole("link", { name: /Review and approve/ })[0];
    await expect(link.getBoundingClientRect().height).toBeGreaterThanOrEqual(44);
  },
};

const LONG = Array.from({ length: 200 }, (_, i) => `line ${i}`).join("\n");
const longArgs = { proposals: [makeProposal({ id: "p1", text: LONG }), makeProposal({ id: "p2", ticket_id: 8, text: LONG })] };

export const LongText: Story = {
  args: longArgs,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.queryByText(/line 150/)).toBeNull();
    const [toggle] = canvas.getAllByRole("button", { name: /Show details/ });
    await userEvent.click(toggle);
    const full = canvas.getByText(/line 150/);
    await expect(full.getBoundingClientRect().height).toBeLessThanOrEqual(window.innerHeight * 0.3 + 1);
    // Clipped, not cut off: the rest is reachable by scrolling inside the text.
    await expect(full.scrollHeight).toBeGreaterThan(full.clientHeight);
    const band = canvas.getByRole("list");
    await expect(band.scrollHeight).toBeGreaterThanOrEqual(band.clientHeight);
    await expect(band.getBoundingClientRect().height).toBeLessThanOrEqual(window.innerHeight * 0.3 + 1);
  },
};

export const LongTextPhone: Story = {
  ...LongText,
  globals: { viewport: { value: "mobile1", isRotated: false } },
};

export const ActionableWhenExpanded: Story = {
  args: { proposals: [makeProposal({ id: "p1", action_line: "Cancel #9.", kind: "status", status: "cancelled", text: "obsolete" })] },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.queryByRole("button", { name: "Approve" })).toBeNull();
    await userEvent.click(canvas.getByRole("button", { name: /Show details/ }));
    await userEvent.click(canvas.getByRole("button", { name: "Approve" }));
    await expect(canvas.getByRole("button", { name: /^Confirm:/ })).toBeVisible();
  },
};

export const ActionableWhenExpandedPhone: Story = {
  ...ActionableWhenExpanded,
  globals: { viewport: { value: "mobile1", isRotated: false } },
};
