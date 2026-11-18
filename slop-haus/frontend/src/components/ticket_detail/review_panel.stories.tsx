import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { ApiError, type Ticket } from "@/api";
import { makeAgentRole, makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import ReviewPanel from "./review_panel";
import { useReviewActions } from "./use_review_actions";
import { useTicketStore } from "./use_ticket_store";
import "./ticket_detail.scss";

const TICKET_ID = 7;

// The panel's drafts and busy/error state live in the page that renders it,
// so the story drives the real store and hook rather than faking the actions.
function Host() {
  const store = useTicketStore(TICKET_ID);
  const review = useReviewActions(store, TICKET_ID);
  if (store.state.status !== "loaded") return <p>Loading…</p>;
  return <ReviewPanel ticket={store.state.ticket} review={review} />;
}

const meta = {
  title: "Ticket detail/ReviewPanel",
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

const PR = "https://example.test/org/repo/pull/9";
const IN_REVIEW = makeTicket({
  id: TICKET_ID,
  status: "review",
  requires_approval: true,
  pr_url: PR,
  resume_agent_id: "agent-42",
});
const MERGED_UNREVIEWED = makeTicket({
  id: TICKET_ID,
  status: "done",
  pr_url: PR,
});

interface Calls {
  filed: { title: string; body?: string }[];
  approved: number;
  changes: string[];
  reviewed: string[];
}
const calls: Calls = { filed: [], approved: 0, changes: [], reviewed: [] };

interface Overrides {
  approve?: () => Promise<Ticket>;
  requestChanges?: (comment: string) => Promise<Ticket>;
  markReviewed?: (comment: string) => Promise<Ticket>;
}

// Stateful: each answer moves the ticket, so the next read shows it.
function stubTicket(initial: Ticket, overrides: Overrides = {}) {
  let current = initial;
  calls.filed = [];
  calls.approved = 0;
  calls.changes = [];
  calls.reviewed = [];
  stubApi({
    streamBoardChanges: () => () => {},
    getTicket: async () => current,
    listComments: async () => [],
    listAgentContexts: async () => [],
    listTicketAttachments: async () => [],
    listRevisions: async () => [],
    listAttemptReviewRuns: async () => [],
    listStatusHistory: async () => [],
    listAgents: async () => [],
    listAgentRoles: async () => [makeAgentRole({ id: "r1", name: "reviewer" })],
    getOnboardingStatus: async () => ({ steps: [], code_work_ready: true }),
    listVariantOptions: async () => [],
    createTicket: async (req) => {
      calls.filed.push(req);
      return makeTicket({ id: 99, title: req.title });
    },
    approveTicket: async () => {
      calls.approved++;
      if (overrides.approve) return overrides.approve();
      current = {
        ...current,
        status: "open",
        approved_at: "2026-02-01T10:00:00Z",
      };
      return current;
    },
    requestTicketChanges: async (_id, comment) => {
      calls.changes.push(comment);
      if (overrides.requestChanges) return overrides.requestChanges(comment);
      current = {
        ...current,
        status: "open",
        changes_requested_at: "2026-02-01T10:00:00Z",
      };
      return current;
    },
    markTicketPostReviewed: async (_id, comment = "") => {
      calls.reviewed.push(comment);
      if (overrides.markReviewed) return overrides.markReviewed(comment);
      current = {
        ...current,
        post_reviewed_at: "2026-02-01T10:00:00Z",
        post_reviewed_by_user_id: "user-1",
      };
      return current;
    },
  });
}

export const InReview: Story = {
  beforeEach: () => stubTicket(IN_REVIEW),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByText("Waiting on your review."),
    ).toBeVisible();
    await expect(canvas.getByRole("link", { name: PR })).toHaveAttribute(
      "href",
      PR,
    );
    await expect(canvas.getByRole("status")).toHaveTextContent("agent-42");
    await expect(canvas.getByRole("button", { name: "Approve" })).toBeEnabled();
    await expect(canvas.getByLabelText("What needs to change")).toHaveValue("");
    await expect(
      canvas.getByRole("button", { name: "Request changes" }),
    ).toBeEnabled();
  },
};

export const Approve: Story = {
  beforeEach: () => stubTicket(IN_REVIEW),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Approve" }),
    );
    await expect(
      await canvas.findByText(/the agent will merge it/),
    ).toHaveTextContent("Approved");
    await expect(canvas.queryByRole("button", { name: "Approve" })).toBeNull();
    await expect(calls).toEqual({
      filed: [],
      approved: 1,
      changes: [],
      reviewed: [],
    });
  },
};

export const Approving: Story = {
  beforeEach: () =>
    stubTicket(IN_REVIEW, { approve: () => new Promise<Ticket>(() => {}) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Approve" }),
    );
    await expect(
      await canvas.findByRole("button", { name: "Approving…" }),
    ).toBeDisabled();
  },
};

export const ApproveError: Story = {
  beforeEach: () =>
    stubTicket(IN_REVIEW, {
      approve: async () => {
        throw new ApiError(409, "ticket is not in review");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Approve" }),
    );
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "ticket is not in review",
    );
    await expect(canvas.getByRole("button", { name: "Approve" })).toBeEnabled();
  },
};

export const RequestChanges: Story = {
  beforeEach: () => stubTicket(IN_REVIEW),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(
      await canvas.findByLabelText("What needs to change"),
      "  Rename the helper  ",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Request changes" }),
    );
    await expect(await canvas.findByText(/^Changes requested/)).toBeVisible();
    // A different decision from approving, and the comment trimmed.
    await expect(calls).toEqual({
      filed: [],
      approved: 0,
      changes: ["Rename the helper"],
      reviewed: [],
    });
    await expect(canvas.queryByText(/^Approved/)).toBeNull();
  },
};

export const RequestChangesNeedsComment: Story = {
  beforeEach: () => stubTicket(IN_REVIEW),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByLabelText("What needs to change"),
    ).toBeRequired();
    await userEvent.click(
      canvas.getByRole("button", { name: "Request changes" }),
    );
    await expect(calls.changes).toEqual([]);
  },
};

export const SendingChanges: Story = {
  beforeEach: () =>
    stubTicket(IN_REVIEW, {
      requestChanges: () => new Promise<Ticket>(() => {}),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(
      await canvas.findByLabelText("What needs to change"),
      "Fix it",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Request changes" }),
    );
    await expect(
      await canvas.findByRole("button", { name: "Sending…" }),
    ).toBeDisabled();
  },
};

export const RequestChangesError: Story = {
  beforeEach: () =>
    stubTicket(IN_REVIEW, {
      requestChanges: async () => {
        throw new ApiError(500, "database unavailable");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(
      await canvas.findByLabelText("What needs to change"),
      "Fix it",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Request changes" }),
    );
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "database unavailable",
    );
    // The comment is the only copy of what the human wrote.
    await expect(canvas.getByLabelText("What needs to change")).toHaveValue(
      "Fix it",
    );
    await expect(
      canvas.getByRole("button", { name: "Request changes" }),
    ).toBeEnabled();
  },
};

export const Approved: Story = {
  beforeEach: () =>
    stubTicket({
      ...IN_REVIEW,
      status: "open",
      approved_at: "2026-02-01T10:00:00Z",
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByText(/the agent will merge it/),
    ).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "Approve" })).toBeNull();
  },
};

export const ChangesRequested: Story = {
  beforeEach: () =>
    stubTicket({
      ...IN_REVIEW,
      status: "open",
      changes_requested_at: "2026-02-01T10:00:00Z",
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/^Changes requested/)).toBeVisible();
    await expect(
      canvas.queryByRole("button", { name: "Request changes" }),
    ).toBeNull();
  },
};

export const MergedWithoutReview: Story = {
  beforeEach: () => stubTicket(MERGED_UNREVIEWED),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByText("Merged without review."),
    ).toBeVisible();
    await expect(
      canvas.getByRole("button", { name: "Mark reviewed" }),
    ).toBeEnabled();
  },
};

export const MarkReviewed: Story = {
  beforeEach: () => stubTicket(MERGED_UNREVIEWED),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(
      await canvas.findByLabelText("Anything you found (optional)"),
      " Looks fine ",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Mark reviewed" }),
    );
    await expect(
      await canvas.findByText(/^Reviewed after merge by/),
    ).toBeVisible();
    await expect(
      canvas.queryByRole("button", { name: "Mark reviewed" }),
    ).toBeNull();
    await waitFor(() => expect(calls.reviewed).toEqual(["Looks fine"]));
  },
};

export const MarkReviewedError: Story = {
  beforeEach: () =>
    stubTicket(MERGED_UNREVIEWED, {
      markReviewed: async () => {
        throw new ApiError(500, "database unavailable");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(
      await canvas.findByLabelText("Anything you found (optional)"),
      "Typo",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: "Mark reviewed" }),
    );
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "database unavailable",
    );
    await expect(
      canvas.getByLabelText("Anything you found (optional)"),
    ).toHaveValue("Typo");
    await expect(
      canvas.getByRole("button", { name: "Mark reviewed" }),
    ).toBeEnabled();
  },
};

// Each of these has already been looked at (or didn't need it), so none
// should be asked for a post-merge review.
const NOT_AWAITING: Record<string, Partial<Ticket>> = {
  RequiredApproval: { requires_approval: true },
  Approved: { approved_at: "2026-02-01T10:00:00Z" },
  AlreadyReviewed: {
    post_reviewed_at: "2026-02-01T10:00:00Z",
    post_reviewed_by_user_id: "user-1",
  },
};

export const NoPostReviewPrompt: Story = {
  beforeEach: () =>
    stubTicket({ ...MERGED_UNREVIEWED, ...NOT_AWAITING.RequiredApproval }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("button", { name: "File a follow-up ticket" });
    await expect(canvas.queryByText("Merged without review.")).toBeNull();
  },
};

export const ApprovedBeforeMerge: Story = {
  beforeEach: () =>
    stubTicket({ ...MERGED_UNREVIEWED, ...NOT_AWAITING.Approved }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/^Approved/)).toBeVisible();
    await expect(canvas.queryByText("Merged without review.")).toBeNull();
  },
};

export const ReviewedAfterMerge: Story = {
  beforeEach: () =>
    stubTicket({ ...MERGED_UNREVIEWED, ...NOT_AWAITING.AlreadyReviewed }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByText(/^Reviewed after merge by/),
    ).toBeVisible();
    await expect(canvas.queryByText("Merged without review.")).toBeNull();
  },
};

export const FollowUp: Story = {
  beforeEach: () => stubTicket(MERGED_UNREVIEWED),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "File a follow-up ticket" }),
    );
    // Prefilled from the ticket being reviewed.
    await expect(
      await canvas.findByRole("textbox", { name: "Title" }),
    ).toHaveValue(`Follow-up to #${TICKET_ID}: `);
    await userEvent.type(
      canvas.getByRole("textbox", { name: "Title" }),
      "tidy up",
    );
    await userEvent.click(canvas.getByRole("button", { name: "Open" }));
    await expect(
      await canvas.findByRole("link", { name: "#99" }),
    ).toBeVisible();
    await expect(calls.filed).toHaveLength(1);
    await expect(calls.filed[0].title).toBe(
      `Follow-up to #${TICKET_ID}: tidy up`,
    );
    await expect(calls.filed[0].body).toContain(PR);
  },
};
