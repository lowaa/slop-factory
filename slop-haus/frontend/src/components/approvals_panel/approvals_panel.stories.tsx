import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import type { Proposal } from "@/api";
import { makeEditProposal, makeManifestProposal, makeProposal, makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import { ProposalsProvider } from "@/components/proposals";
import { DEFAULT_APPROVALS_VALUES } from "@/domain/approvals_filter";
import ApprovalsPanel from "./approvals_panel";

const answer = makeProposal({ id: "p-answer", ticket_id: 7, kind: "discussion_answer" });
const ready = makeProposal({
  id: "p-ready",
  ticket_id: 8,
  ticket_title: "Plan the thing",
  kind: "ready",
  ticket_status: "preparing",
  text: "",
  action_line: "Move #8 out of preparing and into the queue, so an agent can pick it up.",
});
const status = makeProposal({
  id: "p-status",
  ticket_id: 9,
  ticket_title: "Retire the old importer",
  kind: "status",
  ticket_status: "in_progress",
  text: "Blocked on the vendor's export format.",
  action_line: "Hand #9 back, with the reason below as your comment.",
});

const meta = {
  title: "Panels/ApprovalsPanel",
  component: ApprovalsPanel,
  args: { values: DEFAULT_APPROVALS_VALUES, onFilterChange: fn() },
  decorators: [
    (Story) => (
      <ProposalsProvider>
        <Story />
      </ProposalsProvider>
    ),
  ],
  // The panel also follows the board's stream; nothing arrives in a story.
  beforeEach: (ctx) => {
    ctx.pending = [] as Proposal[];
    stubApi({
      listProposals: async () => ctx.pending,
      listPendingProposalsPage: async () => ({ proposals: [...ctx.pending].reverse() }),
      streamBoardChanges: () => () => {},
    });
  },
} satisfies Meta<typeof ApprovalsPanel>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NothingPending: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("Nothing waiting for you.")).toBeInTheDocument();
    await expect(canvas.queryByRole("button", { name: /Reject all/ })).not.toBeInTheDocument();
  },
};

export const SeveralPending: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [answer, ready, status];
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findAllByRole("group", { name: /^Proposal on/ })).toHaveLength(3);
    await expect(canvas.getByRole("button", { name: "Approve all to open (1)" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Approve all answers (1)" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Reject all (3)" })).toBeEnabled();
  },
};

// A proposal about the workspace has no ticket to link to.
export const WorkspaceProposal: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [
      makeProposal({
        id: "p-workspace",
        ticket_id: null,
        ticket_title: "",
        subject: "tenant",
        approver_role: "owner",
        text: "",
        action_line: "Apply the change below to the workspace.",
      }),
    ];
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const card = within(await canvas.findByRole("group", { name: "Proposal on the workspace" }));
    await expect(card.getByText("Workspace")).toBeInTheDocument();
    await expect(card.queryByRole("link")).not.toBeInTheDocument();
  },
};

// An edit shows exactly what approving writes.
export const EditWithDiff: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [makeEditProposal()];
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const changes = within(await canvas.findByRole("region", { name: /^Changes to #/ }));
    await expect(changes.getByText("- Keep the exporter.")).toBeInTheDocument();
    await expect(changes.getByText("+ Keep the exporter and its tests.")).toBeInTheDocument();
    await expect(changes.getByText("+ docs/")).toBeInTheDocument();
    await expect(changes.getByText("- legacy/")).toBeInTheDocument();
    await expect(changes.getByRole("note")).toHaveTextContent("Docs overhaul");
    await expect(canvas.getByRole("button", { name: "Approve" })).toBeEnabled();
  },
};

// A manifest proposal shows the guards it loosens, rule by rule.
export const ManifestEdit: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [makeManifestProposal()];
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const changes = within(await canvas.findByRole("region", { name: "Changes to the workspace manifest" }));
    await expect(changes.getByText("+ db/migrations/ (migration)")).toBeInTheDocument();
    await expect(changes.getByText("removed")).toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Approve" })).toBeEnabled();
  },
};

// An edit that opens the ticket says so, beside the changes.
export const EditThatOpens: Story = {
  beforeEach: (ctx) => {
    const edit = makeEditProposal();
    ctx.pending = [{ ...edit, edit: { ...edit.edit, open: true } }];
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const changes = within(await canvas.findByRole("region", { name: /^Changes to #/ }));
    await expect(changes.getByText("and moves it to open")).toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Approve" })).toBeEnabled();
  },
};

// Approving calls the API and the card leaves with the next list.
export const ApprovingOne: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [answer, ready];
    ctx.approve = fn(async (id: string) => {
      ctx.pending = ctx.pending.filter((p: Proposal) => p.id !== id);
      return makeProposal({ id, state: "approved" });
    });
    stubApi({ approveProposal: ctx.approve });
  },
  play: async ({ canvasElement, approve }) => {
    const canvas = within(canvasElement);
    const card = within(await canvas.findByRole("group", { name: "Proposal on #8" }));
    await userEvent.click(card.getByRole("button", { name: "Approve" }));
    await expect(approve).toHaveBeenCalledWith("p-ready");
    await waitFor(() => expect(canvas.getAllByRole("group", { name: /^Proposal on/ })).toHaveLength(1));
    await expect(canvas.queryByRole("group", { name: "Proposal on #8" })).not.toBeInTheDocument();
  },
};

// The steward's draft sits on the card; applying it calls the owner endpoint.
export const StewardDraftApplied: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [
      makeProposal({
        id: "p-ready",
        ticket_id: 8,
        kind: "ready",
        text: "",
        draft: {
          applied: false,
          body: "",
          category: "ready_to_queue",
          reason: "The spec is complete.",
          revision: 1,
          would_send_back: false,
          evidence: [{ check: "spec_complete", ok: true, ref: "ticket-8" }],
        },
      }),
    ];
    ctx.apply = fn(async (id: string) => {
      ctx.pending = [];
      return makeProposal({ id, state: "approved" });
    });
    stubApi({ applyStewardDraft: ctx.apply });
  },
  play: async ({ canvasElement, apply }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("✓ spec_complete (Ref: ticket-8)")).toBeInTheDocument();
    await userEvent.click(canvas.getByRole("button", { name: "Apply steward's draft" }));
    await expect(apply).toHaveBeenCalledWith("p-ready");
    await waitFor(() => expect(canvas.queryByRole("group", { name: "Proposal on #8" })).not.toBeInTheDocument());
  },
};

// Cards of different heights (a long folded text, an edit's diff) in a list
// longer than the window: only some are drawn, and the page can load more.
export const ManyPending: Story = {
  beforeEach: (ctx) => {
    ctx.pending = Array.from({ length: 60 }, (_, i) =>
      i % 3 === 0
        ? makeEditProposal({ id: `p-${i}`, ticket_id: 100 + i })
        : makeProposal({ id: `p-${i}`, ticket_id: 100 + i, kind: "discussion_answer", text: "x".repeat(i % 3 === 1 ? 40 : 700) }),
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const drawn = await canvas.findAllByRole("group", { name: /^Proposal on/ });
    await expect(drawn.length).toBeGreaterThan(0);
    await expect(drawn.length).toBeLessThan(60);
    await expect(canvas.getByRole("button", { name: "Reject all (60)" })).toBeEnabled();
  },
};

export const RejectReasonOpen: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [ready];
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Reject" }));
    const reason = canvas.getByRole("textbox", { name: /Reason/ });
    await userEvent.type(reason, "Not yet");
    await expect(reason).toHaveValue("Not yet");
    await expect(canvas.getByRole("button", { name: "Cancel" })).toBeEnabled();
  },
};

export const RejectWithReason: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [ready];
    ctx.reject = fn(async (id: string) => {
      ctx.pending = [];
      return makeProposal({ id, state: "rejected" });
    });
    stubApi({ rejectProposal: ctx.reject });
  },
  play: async ({ canvasElement, reject }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Reject" }));
    const reason = canvas.getByRole("textbox", { name: /Reason/ });
    await userEvent.type(reason, "Not yet");
    await expect(reason).toHaveValue("Not yet");
    await userEvent.click(canvas.getByRole("button", { name: "Reject" }));
    await expect(reject).toHaveBeenCalledWith("p-ready", "Not yet");
    await expect(await canvas.findByText("Nothing waiting for you.")).toBeInTheDocument();
  },
};

export const RejectCancelled: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [ready];
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Reject" }));
    await userEvent.click(canvas.getByRole("button", { name: "Cancel" }));
    await expect(canvas.queryByRole("textbox")).not.toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Approve" })).toBeInTheDocument();
  },
};

// Under 720px the buttons go full width and thumb-sized.
export const PhoneWidth: Story = {
  globals: { viewport: { value: "mobile1", isRotated: false } },
  beforeEach: (ctx) => {
    ctx.pending = [answer, ready];
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(window.matchMedia("(width <= 720px)").matches).toBe(true);
    const approve = (await canvas.findAllByRole("button", { name: "Approve" }))[0];
    await expect(approve.getBoundingClientRect().height).toBeGreaterThanOrEqual(44);
    // Full width: the button fills its card's content box.
    const card = approve.closest(".approvals__card")!;
    const content = card.clientWidth - parseFloat(getComputedStyle(card).paddingLeft) * 2;
    await expect(approve.getBoundingClientRect().width).toBeCloseTo(content, 0);
    await userEvent.click(canvas.getAllByRole("button", { name: "Reject" })[0]);
    await expect(canvas.getByRole("textbox", { name: /Reason/ })).toBeVisible();
  },
};

// Batch buttons say they act on the proposals the filter shows, and name
// only those: the one on a parked ticket stays out of "Approve all".
export const BatchActsOnShown: Story = {
  beforeEach: (ctx) => {
    const hidden = makeProposal({ id: "p-hidden", ticket_id: 12, kind: "ready", ticket_status: "parked" });
    ctx.approveAll = fn(async () => []);
    stubApi({
      listProposals: async () => [hidden, ready],
      listPendingProposalsPage: async () => ({ proposals: [ready] }),
      approveAllProposals: ctx.approveAll,
      streamBoardChanges: () => () => {},
    });
  },
  play: async ({ canvasElement, approveAll }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/act on the 1 shown, not on all 2 pending/)).toBeInTheDocument();
    await userEvent.click(canvas.getByRole("button", { name: "Approve all to open (1)" }));
    await expect(approveAll).toHaveBeenCalledWith("ready", ["p-ready"]);
  },
};

export const HiddenByFilter: Story = {
  beforeEach: () => {
    stubApi({
      listProposals: async () => [answer],
      listPendingProposalsPage: async () => ({ proposals: [] }),
      streamBoardChanges: () => () => {},
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/1 pending proposal is hidden by the filters/)).toBeInTheDocument();
  },
};

// Approving an edit to the spec while two attempts run holds the approval for
// a warning first.
export const EditWarnsWhileAttemptsRun: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [makeEditProposal()];
    const attempt = (number: number, started: number) => ({
      number, status: "in_progress" as const, promoted: number === 1, promoted_at: null, promoted_by_user_id: null,
      pr_url: null, approved_at: null, approved_by_user_id: null, changes_requested_at: null, review_started_at: null,
      started_revision: started, shadow: number > 1, holder: null, variant: null, variant_settable: false, worker_cost: [], runs: [], signals: null, verdict: null, discussion: null,
    });
    stubApi({
      listProposals: async () => ctx.pending,
      listPendingProposalsPage: async () => ({ proposals: [...ctx.pending].reverse() }),
      streamBoardChanges: () => () => {},
      getTicket: async () => makeTicket({ id: 12, attempts: [attempt(1, 3), attempt(2, 5)] }),
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Approve" }));
    await expect(await canvas.findByText(/Attempt 1 started from revision 3, attempt 2 from revision 5/)).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Approve anyway" })).toBeEnabled();
  },
};

// A proposal whose ticket moved on can only be dismissed.
export const StaleProposal: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [makeProposal({ id: "p-stale", ticket_id: 7, state: "stale", stale_reason: "the ticket was edited" })];
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("note")).toHaveTextContent("The ticket has changed since this was proposed (the ticket was edited)");
    await expect(canvas.getByRole("button", { name: "Dismiss" })).toBeEnabled();
    await expect(canvas.queryByRole("button", { name: /^Approve/ })).toBeNull();
  },
};

export const LoadFails: Story = {
  beforeEach: () => {
    stubApi({
      listProposals: async () => [],
      listPendingProposalsPage: async () => {
        throw new Error("down");
      },
      streamBoardChanges: () => () => {},
    });
  },
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("alert")).toHaveTextContent("Failed to load proposals.");
  },
};

// A batch reports what it did to each proposal.
export const BatchNotice: Story = {
  beforeEach: (ctx) => {
    ctx.pending = [ready];
    stubApi({
      approveAllProposals: async () => [{ id: "p-ready", state: "approved", outcome: "", error: "" }],
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Approve all to open (1)" }));
    await expect(await canvas.findByText("#8: approved")).toBeVisible();
  },
};

// A description edit with no line diff (a new description) shows its text whole.
export const EditWithPlainBody: Story = {
  beforeEach: (ctx) => {
    const edit = makeEditProposal();
    ctx.pending = [{ ...edit, edit: { ...edit.edit, body: { new: "A fresh description." } } }];
  },
  play: async ({ canvasElement }) => {
    const changes = within(await within(canvasElement).findByRole("region", { name: /^Changes to #/ }));
    await expect(changes.getByText("A fresh description.")).toBeVisible();
  },
};
