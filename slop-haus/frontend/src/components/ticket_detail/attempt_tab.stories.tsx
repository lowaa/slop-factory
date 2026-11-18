import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import type { TicketAttempt } from "@/api";
import { makeAttempt, makeComment, makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import AttemptTab from "./attempt_tab";
import { useTicketStore } from "./use_ticket_store";
import "./ticket_detail.scss";

const TICKET_ID = 42;

// The tab's drafts and actions live in hooks of the page that renders it, so
// the story drives the real store.
function Host({ number }: { number: number }) {
  const store = useTicketStore(TICKET_ID);
  if (store.state.status !== "loaded") return <p>Loading…</p>;
  const { ticket, comments, contexts } = store.state;
  const attempt = ticket.attempts?.find((a) => a.number === number);
  if (!attempt) return <p>No such attempt.</p>;
  return (
    <AttemptTab
      store={store}
      ticket={ticket}
      attempt={attempt}
      comments={comments}
      contexts={contexts}
      variants={{ options: [], loadFailed: false }}
    />
  );
}

const meta = {
  title: "Ticket detail/AttemptTab",
  component: Host,
  parameters: { layout: "padded" },
} satisfies Meta<typeof Host>;

export default meta;
type Story = StoryObj<typeof meta>;

function stubAttempts(attempts: TicketAttempt[]) {
  stubApi({
    streamBoardChanges: () => () => {},
    getTicket: async () => makeTicket({ id: TICKET_ID, attempts }),
    listComments: async () => [
      makeComment({ id: 1, attempt_number: attempts[0].number, body: "Told this attempt to use a map." }),
    ],
    listAgentContexts: async () => [],
    listRevisions: async () => [],
    listAttemptReviewRuns: async () => [],
    listStatusHistory: async () => [],
    listVariantOptions: async () => [],
  });
}

const OTHERS = [
  makeAttempt({ number: 1, status: "queued" }),
  makeAttempt({ number: 2, status: "queued", promoted: false }),
];

export const InDiscussion: Story = {
  args: { number: 3 },
  beforeEach: () => stubAttempts([...OTHERS, makeAttempt({ number: 3, status: "discussion", promoted: false })]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Answer attempt 3" })).toBeInTheDocument();
  },
};

export const InReview: Story = {
  args: { number: 2 },
  beforeEach: () =>
    stubAttempts([
      makeAttempt({ number: 1, status: "queued" }),
      makeAttempt({ number: 2, status: "review", promoted: false, shadow: true, pr_url: "https://github.com/o/r/pull/9" }),
    ]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Approve attempt 2" })).toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Request changes on attempt 2" })).toBeInTheDocument();
  },
};

// Past the seven colours the palette wraps: attempt 8 is attempt 1's colour,
// told apart by its number.
export const WrappedColour: Story = {
  args: { number: 8 },
  beforeEach: () => stubAttempts([makeAttempt({ number: 8, status: "in_progress", promoted: false })]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Comment on attempt 8" })).toBeInTheDocument();
  },
};

export const CloseQueued: Story = {
  args: { number: 2 },
  beforeEach: () => stubAttempts(OTHERS),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Close attempt 2" }));
    await expect(canvas.getByRole("group", { name: "Confirm closing attempt 2" })).toHaveTextContent(
      "It will not be started.",
    );
  },
};

export const CloseRunning: Story = {
  args: { number: 2 },
  beforeEach: () =>
    stubAttempts([
      makeAttempt({ number: 1, status: "in_progress", started_revision: 1 }),
      makeAttempt({ number: 2, status: "in_progress", promoted: false, shadow: true, started_revision: 1 }),
    ]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Close attempt 2" }));
    await expect(canvas.getByRole("group", { name: "Confirm closing attempt 2" })).toHaveTextContent(
      "Its work will be stopped and its PR closed.",
    );
  },
};

export const Closed: Story = {
  args: { number: 2 },
  beforeEach: () => stubAttempts([OTHERS[0], makeAttempt({ number: 2, status: "closed", promoted: false })]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/This attempt was closed/)).toBeInTheDocument();
    await expect(canvas.queryByRole("button", { name: /Close attempt/ })).toBeNull();
  },
};

export const PromoteRunning: Story = {
  args: { number: 2 },
  beforeEach: () =>
    stubAttempts([
      makeAttempt({ number: 1, status: "in_progress", started_revision: 1 }),
      makeAttempt({ number: 2, status: "in_progress", promoted: false, shadow: true, started_revision: 1 }),
    ]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Promote attempt 2" }));
    await expect(canvas.getByRole("group", { name: "Confirm promoting attempt 2" })).toHaveTextContent(
      "Attempt 1 is promoted now: it is demoted",
    );
  },
};

export const PromoteAwaitingChoice: Story = {
  args: { number: 2 },
  beforeEach: () =>
    stubAttempts([
      makeAttempt({ number: 1, status: "review", promoted: false, promoted_at: null }),
      makeAttempt({ number: 2, status: "review", promoted: false, promoted_at: null }),
    ]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Promote attempt 2" }));
    await expect(canvas.getByRole("group", { name: "Confirm promoting attempt 2" })).toHaveTextContent(
      "goes back to its agent to merge",
    );
  },
};
