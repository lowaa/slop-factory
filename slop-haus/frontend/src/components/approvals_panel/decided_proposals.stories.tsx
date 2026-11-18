import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { makeMember, makeProposal } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import { MemberNamesProvider } from "@/shared/names/member_name";
import DecidedProposals, { DecidedCard } from "./decided_proposals";

const USER_ID = "33333333-3333-3333-3333-333333333333";
const decided = { decided_at: "2026-01-02T09:30:00Z", decided_by_user_id: USER_ID };

const autoApproved = makeProposal({ id: "p-auto", state: "approved", outcome: "auto-approved", ...decided });
const approvedByPerson = makeProposal({ id: "p-person", ticket_id: 8, state: "approved", ...decided });
const rejected = makeProposal({ id: "p-rej", ticket_id: 9, state: "rejected", outcome: "Not yet", ...decided });
const stale = makeProposal({
  id: "p-stale",
  ticket_id: 10,
  state: "stale",
  stale_reason: "the ticket was edited after this was proposed",
  ...decided,
});
const departed = makeProposal({
  id: "p-gone",
  ticket_id: 11,
  state: "approved",
  decided_at: decided.decided_at,
  decided_by_user_id: null,
  decided_by_user_id_deleted: true,
});

// The app has one dark theme (index.scss), which the preview loads, so there
// is no second theme to show these in.
const meta = {
  title: "Panels/DecidedProposals",
  component: DecidedProposals,
  args: { filter: { status: ["open"] } },
  decorators: [
    (Story) => (
      <MemberNamesProvider currentUserId="99999999-9999-9999-9999-999999999999">
        <Story />
      </MemberNamesProvider>
    ),
  ],
  beforeEach: () =>
    stubApi({
      listMemberNames: async () => ({ members: [makeMember({ id: USER_ID, display_name: "Grace Hopper" })], former: [] }),
      listDecidedProposalsPage: async () => ({ proposals: [autoApproved, approvedByPerson, departed] }),
    }),
} satisfies Meta<typeof DecidedProposals>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Approved: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("group", { name: "Decided proposal on #7" })).toBeVisible();
    await expect(canvas.getByText("auto-approved", { selector: ".decided__auto" })).toBeVisible();
    await expect(canvas.getByText("Deleted user")).toBeVisible();
    await expect(canvas.queryByRole("button", { name: /Approve|Reject/ })).not.toBeInTheDocument();
  },
};

export const ChooseRejected: Story = {
  beforeEach: () =>
    stubApi({ listDecidedProposalsPage: async (_f, state) => ({ proposals: state === "rejected" ? [rejected] : [autoApproved] }) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("combobox", { name: "State" }));
    await userEvent.click(await within(canvasElement.ownerDocument.body).findByRole("option", { name: "Rejected" }));
    await expect(await canvas.findByRole("group", { name: "Decided proposal on #9" })).toBeVisible();
    await expect(canvas.getByText("Not yet")).toBeVisible();
  },
};

export const Empty: Story = {
  beforeEach: () => stubApi({ listDecidedProposalsPage: async () => ({ proposals: [] }) }),
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByText("No approved proposals.")).toBeVisible();
  },
};

export const LoadFails: Story = {
  beforeEach: () =>
    stubApi({
      listDecidedProposalsPage: async () => {
        throw new Error("down");
      },
    }),
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("alert")).toHaveTextContent("Failed to load proposals.");
  },
};

// One card per variant, side by side.
export const CardVariants: StoryObj<typeof DecidedCard> = {
  render: () => (
    <MemberNamesProvider currentUserId="99999999-9999-9999-9999-999999999999">
      {[autoApproved, approvedByPerson, rejected, stale].map((p) => (
        <DecidedCard key={p.id} proposal={p} />
      ))}
    </MemberNamesProvider>
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findAllByRole("group", { name: /^Decided proposal on/ })).toHaveLength(4);
    await expect(canvas.getByText(/the ticket was edited/)).toBeVisible();
    await expect(canvas.getByText("Rejected")).toBeVisible();
  },
};
