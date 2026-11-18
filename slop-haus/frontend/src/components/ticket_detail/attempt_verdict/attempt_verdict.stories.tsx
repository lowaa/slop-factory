import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import type { AttemptVerdict as Verdict, TicketAttempt } from "@/api";
import { makeAttempt, makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import { useTicketStore } from "../use_ticket_store";
import AttemptVerdict from "./attempt_verdict";
import "../ticket_detail.scss";
import "./attempt_verdict.scss";

const TICKET_ID = 7;

// The verdict is written through the page's store, so the story drives the
// real one.
function Host() {
  const store = useTicketStore(TICKET_ID);
  if (store.state.status !== "loaded") return <p>Loading…</p>;
  const attempt = store.state.ticket.attempts?.[0] as TicketAttempt;
  return <AttemptVerdict store={store} ticketId={TICKET_ID} attempt={attempt} />;
}

const meta = {
  title: "Ticket detail/AttemptVerdict",
  component: Host,
  parameters: { layout: "padded" },
} satisfies Meta<typeof Host>;

export default meta;
type Story = StoryObj<typeof meta>;

function stubTicket(attempt: TicketAttempt, extra: Parameters<typeof stubApi>[0] = {}) {
  stubApi({
    streamBoardChanges: () => () => {},
    getTicket: async () => makeTicket({ id: TICKET_ID, attempts: [attempt] }),
    listComments: async () => [],
    listAgentContexts: async () => [],
    listTicketAttachments: async () => [],
    listRevisions: async () => [],
    listAttemptReviewRuns: async () => [],
    listStatusHistory: async () => [],
    listVariantOptions: async () => [],
    ...extra,
  });
}

const verdict = (overrides: Partial<Verdict> = {}): Verdict => ({
  verdict: "would_merge",
  note: "",
  set_at: "2026-10-30T10:00:00Z",
  set_by_user_id: null,
  history: [],
  ...overrides,
});

export const Unjudged: Story = {
  beforeEach: () =>
    stubTicket(makeAttempt({ status: "review" }), {
      setAttemptVerdict: async (_id, _n, v, note) => verdict({ verdict: v, note }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("No verdict yet.")).toBeVisible();
    await userEvent.type(canvas.getByLabelText(/Note/), "rename it");
    await userEvent.click(canvas.getByRole("button", { name: "Would merge with changes" }));
    await expect(await canvas.findByText("rename it")).toBeVisible();
  },
};

export const ChangedMind: Story = {
  beforeEach: () =>
    stubTicket(
      makeAttempt({
        status: "done",
        verdict: verdict({
          verdict: "would_not_merge",
          note: "breaks the build",
          history: [verdict({ note: "first impression", set_at: "2026-10-29T10:00:00Z" })],
        }),
      }),
    ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("breaks the build")).toBeVisible();
    await userEvent.click(canvas.getByText("Earlier verdicts (1)"));
    await expect(canvas.getByText("first impression")).toBeVisible();
  },
};
