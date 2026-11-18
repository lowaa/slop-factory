import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import type { CloseAsMergedResult, MergeFacts, Ticket } from "@/api";
import { makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import CloseAsMergedPanel from "./close_as_merged";
import { useCloseAsMerged } from "./use_close_as_merged";
import { useTicketStore } from "./use_ticket_store";
import "./ticket_detail.scss";

const TICKET_ID = 7;

// The facts and the confirmation live in the page that renders the panel, so
// the story drives the real store and hook.
function Host() {
  const store = useTicketStore(TICKET_ID);
  const action = useCloseAsMerged(store, TICKET_ID);
  if (store.state.status !== "loaded") return <p>Loading…</p>;
  return <CloseAsMergedPanel ticket={store.state.ticket} action={action} />;
}

const meta = {
  title: "Ticket detail/CloseAsMergedPanel",
  component: Host,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 640 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof Host>;

export default meta;
type Story = StoryObj<typeof meta>;

const PR = "https://example.test/org/repo/pull/12";
const SHA = "a".repeat(40);
const IN_REVIEW = makeTicket({ id: TICKET_ID, status: "review", pr_url: PR });

const GREEN: MergeFacts = {
  pr_url: PR,
  base: "main",
  merged: true,
  merged_into_base: true,
  merged_by: "merge-bot",
  merge_sha: "c".repeat(40),
  merged_head: SHA,
  approved_head: SHA,
  head_approved: true,
  checks: "passed",
  merge_on_base: true,
  confirmations: [],
  ignored: {
    merger_login: "merge-bot",
    reason: "the merger, GitHub user merge-bot (id 77), is not a member here with that GitHub account linked",
    at: "2026-10-27T10:00:00Z",
  },
};

const DOUBTFUL: MergeFacts = {
  ...GREEN,
  head_approved: false,
  approved_head: "b".repeat(40),
  checks: "failed",
  confirmations: [
    "the checks on the merged head aaaaaaaaaaaa did not pass (failed)",
    "the merged head aaaaaaaaaaaa is not the approved head bbbbbbbbbbbb",
  ],
};

function stubFacts(facts: MergeFacts, close: (confirm: boolean) => Promise<CloseAsMergedResult>) {
  let current: Ticket = IN_REVIEW;
  stubApi({
    streamBoardChanges: () => () => {},
    getTicket: async () => current,
    listComments: async () => [],
    listAgentContexts: async () => [],
    listTicketAttachments: async () => [],
    listRevisions: async () => [],
    listAttemptReviewRuns: async () => [],
    getTicketMergeFacts: async () => facts,
    closeTicketAsMerged: async (_id, confirm = false) => {
      const result = await close(confirm);
      if (result.ticket) current = result.ticket;
      return result;
    },
  });
}

const closed: CloseAsMergedResult = {
  closed: true,
  needs_confirmation: false,
  confirmations: [],
  ticket: makeTicket({ id: TICKET_ID, status: "done", pr_url: PR }),
};

export const UnlinkedMerger: Story = {
  beforeEach: () => stubFacts(GREEN, async () => closed),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/PR #12 was merged by merge-bot, so the ticket wasn't closed\./)).toBeVisible();
    await expect(canvas.getByText(/is the approved head/)).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Close as merged" })).toBeEnabled();
  },
};

export const ConfirmsANotGreenHead: Story = {
  beforeEach: () =>
    stubFacts(DOUBTFUL, async (confirm) =>
      confirm ? closed : { closed: false, needs_confirmation: true, confirmations: DOUBTFUL.confirmations },
    ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Close as merged" }));
    const dialog = await canvas.findByRole("alertdialog", { name: "Confirm close as merged" });
    await expect(within(dialog).getByText(/did not pass \(failed\)/)).toBeVisible();
    await expect(within(dialog).getByText(/is not the approved head/)).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Close as merged anyway" })).toBeEnabled();
  },
};

export const NoRecordedMerge: Story = {
  beforeEach: () => stubFacts({ ...GREEN, ignored: undefined }, async () => closed),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Close as merged" })).toBeEnabled();
    await expect(canvas.queryByText(/so the ticket wasn't closed/)).toBeNull();
  },
};
