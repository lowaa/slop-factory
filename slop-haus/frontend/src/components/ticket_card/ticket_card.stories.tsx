import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { TestRouterProvider } from "@slop-factory/router/testing/provider";
import { AuthContext } from "@/context/auth";
import { makeAttemptCard, makePath, makeTicket } from "@/test/fixtures";
import { signedInAuth } from "@/test/render_signed_in";
import TicketCard from "./ticket_card";

// Framed at the board column's narrowest width: a card is only ever as
// wide as its column, and that is where long unbroken paths and titles
// used to spill out of the card.
//
// The card is a router Link that names the session's tenant, so the story
// needs both a router and a signed-in auth context to render at all --
// the same pair render_signed_in.tsx's renderSignedIn gives component tests.
const meta = {
  title: "Widgets/TicketCard",
  component: TicketCard,
  parameters: { layout: "centered" },
  decorators: [
    (Story) => (
      <TestRouterProvider>
        <AuthContext.Provider value={signedInAuth()}>
          <div style={{ width: 240 }}>
            <Story />
          </div>
        </AuthContext.Provider>
      </TestRouterProvider>
    ),
  ],
} satisfies Meta<typeof TicketCard>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  args: {
    ticket: makeTicket({ paths: [makePath({ path: "server/internal/ticket/" })] }),
  },
};

export const LongPaths: Story = {
  args: {
    ticket: makeTicket({
      priority: "high",
      status: "discussion",
      needs_discussion: true,
      title: "slop-haus: the ticket widgets on the board page overflow their div",
      paths: [
        makePath({ path: "slop-haus/frontend/src/components/ticket_card/ticket_card.scss" }),
        makePath({ path: "slop-haus/frontend/src/components/some_really_long_component_name_without_breaks/" }),
      ],
    }),
  },
};

export const LongUnbrokenTitle: Story = {
  args: {
    ticket: makeTicket({ title: "slop-haus/frontend/src/components/ticket_card/ticket_card.tsx is broken" }),
  },
};

export const InReview: Story = {
  args: {
    ticket: makeTicket({
      status: "review",
      requires_approval: true,
      title: "Add the review status to the board",
      pr_url: "https://example.test/org/repo/pull/21",
    }),
  },
};

export const Parked: Story = {
  args: {
    ticket: makeTicket({ status: "parked", title: "Rewrite the board in a different framework" }),
  },
};

const COST = [
  { billing_mode: "metered", cost_micros: 1_240_000, input_tokens: 1, output_tokens: 1, requests: 3, unpriced_records: 0 },
];

export const AttemptInProgress: Story = {
  args: {
    ticket: makeAttemptCard(
      { id: 42, title: "Add the review status to the board", pr_url: "https://example.test/org/repo/pull/21" },
      { number: 2, total: 3, worker_cost: COST },
    ),
  },
};

export const AttemptInDiscussion: Story = {
  args: {
    ticket: makeAttemptCard(
      { id: 42, title: "Add the review status to the board", status: "discussion" },
      { number: 1, total: 2, status: "discussion", worker_cost: COST },
    ),
  },
};

export const AttemptAwaitingChoice: Story = {
  args: {
    ticket: makeAttemptCard(
      { id: 42, title: "Add the review status to the board", status: "review", pr_url: "https://example.test/org/repo/pull/21" },
      { number: 1, total: 2, status: "review", awaiting_choice: true, worker_cost: COST },
    ),
  },
};

export const NoEligibleAgent: Story = {
  args: { ticket: makeTicket({ title: "Needs a reviewer", no_eligible_agent: true }) },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByText("no eligible agent")).toBeVisible();
  },
};
