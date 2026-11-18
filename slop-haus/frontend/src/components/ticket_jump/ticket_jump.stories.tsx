import { createRef } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { TestRouterProvider } from "@slop-factory/router/testing/provider";
import { AuthContext } from "@/context/auth";
import { makeTicket } from "@/test/fixtures";
import { signedInAuth } from "@/test/render_signed_in";
import TicketJump from "./ticket_jump";
import type { TicketJumpState } from "./use_ticket_jump";

const jump = (result: TicketJumpState["result"]): TicketJumpState => ({
  result,
  linkRef: createRef<HTMLAnchorElement>(),
  open: () => false,
  dismiss: () => false,
});

const meta = {
  title: "Widgets/TicketJump",
  component: TicketJump,
  decorators: [
    (Story) => (
      <TestRouterProvider>
        <AuthContext.Provider value={signedInAuth()}>
          <Story />
        </AuthContext.Provider>
      </TestRouterProvider>
    ),
  ],
} satisfies Meta<typeof TicketJump>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Found: Story = {
  args: { jump: jump({ kind: "found", ticket: makeTicket({ id: 85, title: "Shape the intake form", status: "in_progress" }) }) },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByRole("link", { name: /#85/ })).toBeVisible();
  },
};

export const Missing: Story = {
  args: { jump: jump({ kind: "missing", id: 9999 }) },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByText("No ticket #9999")).toBeVisible();
  },
};
