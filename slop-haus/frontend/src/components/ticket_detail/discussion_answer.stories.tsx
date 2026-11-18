import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, waitFor, within } from "storybook/test";
import type { DiscussionDraft, DiscussionLabel, TicketAttempt } from "@/api";
import { CONTEXT_HINT_TOKENS } from "@/lib/context_level";
import { makeAttempt, makeConductor, makeConversation, makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import DiscussionAnswer from "./discussion_answer";
import "./ticket_detail.scss";

const TICKET_ID = 7;
const TEMPLATE = "Merge conflicts are resolved; carry on and merge.";

const meta = {
  title: "Ticket detail/DiscussionAnswer",
  component: DiscussionAnswer,
  parameters: { layout: "padded" },
  args: { ticketId: TICKET_ID, attempt: makeAttempt() },
} satisfies Meta<typeof DiscussionAnswer>;

export default meta;
type Story = StoryObj<typeof meta>;

function label(overrides: Partial<DiscussionLabel> = {}): DiscussionLabel {
  return {
    label: "routine",
    category: "merge_continue",
    reason: "The worker only asks whether to continue.",
    evidence: [
      { check: "no_open_conflicts", ok: true, ref: "abc123", detail: "clean" },
      { check: "checks_green", ok: true, ref: "pipeline 9", detail: "" },
    ],
    ticket_revision: 3,
    head_sha: "a".repeat(40),
    assessed_at: "2026-10-30T10:00:00Z",
    ...overrides,
  };
}

function stubDiscussion(draft: DiscussionDraft) {
  stubApi({
    listConductors: async () => ({ own: [], others: [] }),
    getDiscussionDraft: async () => draft,
    getTicket: async () => makeTicket({ id: TICKET_ID }),
  });
}

function attemptFor(l: DiscussionLabel): TicketAttempt {
  return makeAttempt({
    status: "discussion",
    discussion: { round: 1, label: l.label, category: l.category, reason: l.reason },
  });
}

const YOURS = label({
  label: "yours",
  category: "",
  reason: "A direction call.",
  evidence: [{ check: "checks_green", ok: false, ref: "pipeline 9", detail: "red" }],
});
const NOT_ASSESSED = label({ label: "not_assessed", category: "", reason: "Daily cap reached.", evidence: [] });

async function open(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.click(await canvas.findByRole("button", { name: "Answer with steward" }));
  return canvas;
}

export const Routine: Story = {
  args: { attempt: attemptFor(label()) },
  beforeEach: () => stubDiscussion({ body: TEMPLATE, label: label() }),
  play: async ({ canvasElement }) => {
    const canvas = await open(canvasElement);
    await expect(await canvas.findByText(TEMPLATE)).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Approve" })).toBeEnabled();
  },
};

export const Yours: Story = {
  args: { attempt: attemptFor(YOURS) },
  beforeEach: () => stubDiscussion({ body: "", label: YOURS }),
  play: async ({ canvasElement }) => {
    const canvas = await open(canvasElement);
    await expect(await canvas.findByRole("list", { name: "Checks" })).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "Approve" })).toBeNull();
  },
};

export const NotAssessed: Story = {
  args: { attempt: attemptFor(NOT_ASSESSED) },
  beforeEach: () => stubDiscussion({ body: "", label: NOT_ASSESSED }),
  play: async ({ canvasElement }) => {
    const canvas = await open(canvasElement);
    await expect(await canvas.findByRole("button", { name: "Assess now" })).toBeEnabled();
  },
};

export const NotLabelledYet: Story = {
  args: { attempt: makeAttempt({ status: "discussion", discussion: { round: 1, label: null } }) },
  beforeEach: () => stubDiscussion({ body: "", label: null }),
  play: async ({ canvasElement }) => {
    const canvas = await open(canvasElement);
    await expect(canvas.getByText("Not labelled yet")).toBeVisible();
    await expect(await canvas.findByRole("button", { name: "Assess now" })).toBeEnabled();
  },
};

export const Assessing: Story = {
  args: { attempt: attemptFor(label({ label: "pending", category: "", reason: "", evidence: [] })) },
  beforeEach: () => stubDiscussion({ body: "", label: label({ label: "pending", category: "", reason: "", evidence: [] }) }),
};

// A phone is ~375px: the buttons wrap instead of overflowing.
export const Phone: Story = {
  args: { attempt: attemptFor(label()) },
  beforeEach: () => stubDiscussion({ body: TEMPLATE, label: label() }),
  decorators: [(Story) => <div style={{ width: 360 }}><Story /></div>],
  play: async ({ canvasElement }) => {
    const canvas = await open(canvasElement);
    await canvas.findByText(TEMPLATE);
    const section = canvasElement.querySelector<HTMLElement>(".ticket-detail__answer")!;
    await expect(section.scrollWidth).toBeLessThanOrEqual(section.clientWidth);
  },
};

export const LargeContext: Story = {
  args: { attempt: attemptFor(label()) },
  beforeEach: () => {
    stubDiscussion({ body: TEMPLATE, label: label() });
    stubApi({
      listConductors: async () => ({ own: [makeConductor({ id: "c1" })], others: [] }),
      getConductorHost: async () => ({ online: true }),
      listConversations: async () => [
        makeConversation({ conductor_id: "c1", state: "ready", context_tokens: CONTEXT_HINT_TOKENS + 1, can_compact: true }),
      ],
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const discuss = await canvas.findByRole("button", { name: "Discuss in Remote Control" });
    await waitFor(() => expect(discuss).toBeEnabled());
    await userEvent.click(discuss);
    await expect(await canvas.findByRole("button", { name: "Clear and discuss" })).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Just discuss" })).toBeVisible();
  },
};
