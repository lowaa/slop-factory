import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import { apiClient, type AgentContext, type Attachment, type Comment, type Ticket } from "@/api";
import { ProposalsProvider } from "@/components/proposals";
import { makeAgentContext, makeAttachment, makeAttempt, makeComment, makeProposal, makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import TicketDetail from "./ticket_detail";
import type { AttemptLink } from "./attempt_view";
import { TICKET_VIEWS, type TicketView } from "./ticket_view";

const TICKET_ID = 42;

// The route keeps the tab in the URL; a story keeps it in state, so the tabs
// and the "all handoffs" button switch it as they do in the app.
function TicketDetailHarness({
  initialView = "main",
  initialAttempt,
}: {
  initialView?: TicketView;
  initialAttempt?: number;
}) {
  const [view, setView] = useState<TicketView>(initialView);
  const [attempt, setAttempt] = useState<number | undefined>(initialAttempt);
  const attemptLink: AttemptLink = (target, { children, ...rest }) => (
    <a
      href={target === null ? "#ticket" : `#attempt-${target}`}
      onClick={(e) => {
        e.preventDefault();
        setAttempt(target ?? undefined);
      }}
      {...rest}
    >
      {children}
    </a>
  );
  return (
    <ProposalsProvider>
      <TicketDetail
        ticketId={TICKET_ID}
        view={view}
        onViewChange={setView}
        attempt={attempt}
        attemptLink={attemptLink}
      />
    </ProposalsProvider>
  );
}

const HANDOFFS: AgentContext[] = [
  makeAgentContext({ id: 1, body: "First pass: read the code." }),
  makeAgentContext({ id: 2, body: "Second pass: plan agreed." }),
];

interface Loaded {
  ticket?: Partial<Ticket>;
  contexts?: AgentContext[];
  comments?: Comment[];
  attachments?: Attachment[];
}

// Everything the page fetches on load. The ticket-level calls a story acts
// on are passed in as extra stubs.
function stubPage({ ticket, contexts = [], comments, attachments = [] }: Loaded, extra: Parameters<typeof stubApi>[0] = {}) {
  stubApi({
    getTicket: async () => makeTicket({ id: TICKET_ID, title: "Wire the thing", ...ticket }),
    listComments: async () => comments ?? [makeComment()],
    listMySettings: async () => [],
    listAgentContexts: async () => contexts,
    listTicketAttachments: async () => attachments,
    listRevisions: async () => [],
    listAttemptReviewRuns: async () => [],
    listStatusHistory: async () => [],
    listAgentRoles: async () => [],
    listVariantOptions: async () => [],
    getTicketLlmUsage: async () => ({ worker: [], guidance: [] }),
    listProposals: async () => [],
    // The discuss shortcut in a discussion ticket reads the machines.
    listConductors: async () => ({ own: [], others: [] }),
    listAgents: async () => [],
    streamBoardChanges: () => () => {},
    getBlockerCandidates: async () => [],
    // What a member gets: nothing to show.
    getTicketMergeFacts: async () => {
      throw new Error("forbidden");
    },
    ...extra,
  });
}

const meta = {
  title: "Ticket detail/Ticket detail",
  component: TicketDetailHarness,
  parameters: { layout: "padded" },
} satisfies Meta<typeof TicketDetailHarness>;

export default meta;
type Story = StoryObj<typeof meta>;

export const OpenTicket: Story = {
  beforeEach: () => stubPage({ ticket: { status: "open", allowed_moves: ["open", "parked", "cancelled"] } }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("heading", { name: "#42 Wire the thing" })).toBeInTheDocument();
    await expect(canvas.getByRole("tab", { name: "Main", selected: true })).toBeInTheDocument();
    await expect(canvas.queryByRole("button", { name: "Open" })).toBeNull();
    await expect(canvas.queryByRole("button", { name: "Unpark" })).toBeNull();
  },
};

export const WithAttachments: Story = {
  beforeEach: () => {
    stubPage({
      attachments: [
        makeAttachment({ id: "i1", filename: "board.png" }),
        makeAttachment({ id: "v1", filename: "demo.mov", content_type: "video/quicktime" }),
      ],
    });
    stubApi({ attachmentUrl: () => "data:image/gif;base64,R0lGODlhAQABAAAAACw=" });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("heading", { name: "Attachments" })).toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Open board.png" })).toBeInTheDocument();
    await expect(canvasElement.querySelector("video[controls]")).not.toBeNull();
  },
};

export const ViewTabs: Story = {
  beforeEach: () => stubPage({ contexts: HANDOFFS }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const tablist = await canvas.findByRole("tablist", { name: "Ticket views" });
    await expect(within(tablist).getAllByRole("tab")).toHaveLength(TICKET_VIEWS.length);

    const history = within(tablist).getByRole("tab", { name: "Agent context history (2)" });
    await userEvent.click(history);
    await expect(history).toHaveAttribute("aria-selected", "true");
    await expect(within(tablist).getByRole("tab", { name: "Main" })).toHaveAttribute("aria-selected", "false");
    await expect(canvas.getByRole("tabpanel", { name: /Agent context history/ })).toBeVisible();
    await expect(canvas.queryByRole("tabpanel", { name: "Main" })).toBeNull();

    await userEvent.click(within(tablist).getByRole("tab", { name: "Main" }));
    await expect(canvas.getByRole("tabpanel", { name: "Main" })).toBeVisible();
  },
};

export const AgentContextView: Story = {
  args: { initialView: "agent-context" },
  beforeEach: () => stubPage({ contexts: HANDOFFS }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("tab", { name: /Agent context history/, selected: true })).toBeInTheDocument();
    // The page hands the history panel what it loaded.
    const panel = within(canvas.getByRole("tabpanel", { name: /Agent context history/ }));
    await expect(panel.getAllByText("Second pass: plan agreed.").length).toBeGreaterThan(0);
    await expect(panel.getAllByText("First pass: read the code.").length).toBeGreaterThan(0);
  },
};

export const PreparingTicket: Story = {
  beforeEach: () => {
    stubPage(
      { ticket: { status: "preparing", can_ready: true } },
      { markTicketReady: fn(async () => makeTicket({ id: TICKET_ID, status: "open" })) },
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Open" }));

    await expect(apiClient.markTicketReady).toHaveBeenCalledWith(TICKET_ID);
    // The reply is the ticket now: the banner is gone with can_ready.
    await waitFor(() => expect(canvas.queryByRole("button", { name: "Open" })).toBeNull());
    await expect(canvas.queryByRole("alert")).toBeNull();
  },
};

export const OpenFailed: Story = {
  beforeEach: () => {
    stubPage(
      { ticket: { status: "preparing", can_ready: true } },
      {
        markTicketReady: async () => {
          throw new Error("server said no");
        },
      },
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Open" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent(/server said no|Failed to open the ticket/);
    await expect(canvas.getByRole("button", { name: "Open" })).toBeEnabled();
  },
};

export const ParkableTicket: Story = {
  beforeEach: () => {
    stubPage(
      { ticket: { status: "open", allowed_moves: ["open", "parked"] } },
      { updateTicket: fn(async () => makeTicket({ id: TICKET_ID, status: "parked", allowed_moves: ["parked", "preparing"] })) },
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Park" }));

    await expect(apiClient.updateTicket).toHaveBeenCalledWith(TICKET_ID, { status: "parked" });
    await expect(await canvas.findByRole("button", { name: "Unpark" })).toBeInTheDocument();
    await expect(canvas.queryByRole("button", { name: "Park" })).toBeNull();
  },
};

export const ParkedTicket: Story = {
  beforeEach: () => {
    stubPage(
      { ticket: { status: "parked", allowed_moves: ["parked", "preparing"] } },
      { updateTicket: fn(async () => makeTicket({ id: TICKET_ID, status: "preparing", can_ready: true })) },
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("Parked.")).toBeInTheDocument();
    await expect(canvas.queryByRole("button", { name: "Park" })).toBeNull();
    await userEvent.click(canvas.getByRole("button", { name: "Unpark" }));

    // Unparking lands in Preparing, never straight in the queue.
    await expect(apiClient.updateTicket).toHaveBeenCalledWith(TICKET_ID, { status: "preparing" });
    await expect(await canvas.findByRole("button", { name: "Open" })).toBeInTheDocument();
    await expect(canvas.queryByRole("button", { name: "Unpark" })).toBeNull();
  },
};

export const CheckboxFields: Story = {
  beforeEach: () => {
    stubPage(
      { ticket: {} },
      {
        updateTicket: fn(async (_id: number, input: Parameters<typeof apiClient.updateTicket>[1]) =>
          makeTicket({
            id: TICKET_ID,
            ...input,
            paths: input.paths?.map((p) => ({ ...p, source: "filed" as const })),
          }),
        ),
      },
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const discussion = await canvas.findByRole("checkbox", { name: "Needs discussion" });
    const approval = canvas.getByRole("checkbox", { name: "Requires human PR approval" });
    await expect(discussion).not.toBeChecked();
    await expect(approval).not.toBeChecked();

    await userEvent.click(discussion);
    await expect(apiClient.updateTicket).toHaveBeenLastCalledWith(TICKET_ID, { needs_discussion: true });
    await waitFor(() => expect(discussion).toBeChecked());
    await expect(approval).not.toBeChecked();

    await userEvent.click(approval);
    await expect(apiClient.updateTicket).toHaveBeenLastCalledWith(TICKET_ID, { requires_approval: true });
    await waitFor(() => expect(approval).toBeChecked());
  },
};

export const AllHandoffs: Story = {
  beforeEach: () => stubPage({ contexts: HANDOFFS }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    // The latest handoff is the one shown on the main tab.
    const latest = within((await canvas.findByRole("heading", { name: "Latest handoff" })).parentElement!);
    await expect(latest.getByText("Second pass: plan agreed.")).toBeInTheDocument();
    await expect(latest.queryByText("First pass: read the code.")).toBeNull();
    await userEvent.click(canvas.getByRole("button", { name: "See all 2 handoffs" }));

    const tab = canvas.getByRole("tab", { name: /Agent context history/ });
    await expect(tab).toHaveAttribute("aria-selected", "true");
    await expect(tab).toHaveFocus();
    await expect(within(canvas.getByRole("tabpanel", { name: /Agent context history/ })).getAllByText("First pass: read the code.").length).toBeGreaterThan(0);
  },
};

export const SingleHandoff: Story = {
  beforeEach: () => stubPage({ contexts: HANDOFFS.slice(0, 1) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const latest = within((await canvas.findByRole("heading", { name: "Latest handoff" })).parentElement!);
    await expect(latest.getByText("First pass: read the code.")).toBeInTheDocument();
    await expect(canvas.queryByRole("button", { name: /handoffs/ })).toBeNull();
  },
};

// A ticket with two attempts, each with something said to it.
const TWO_ATTEMPTS: Partial<Ticket> = {
  status: "discussion",
  attempts: [
    makeAttempt({ number: 1, status: "discussion", promoted: true }),
    makeAttempt({ number: 2, status: "discussion", promoted: false, shadow: true, started_revision: 3 }),
  ],
};
const ATTEMPT_COMMENTS: Comment[] = [
  makeComment({ id: 1, body: "The spec, before work started." }),
  makeComment({ id: 2, attempt_number: 1, body: "Told attempt one to use a map." }),
  makeComment({ id: 3, attempt_number: 2, body: "Told attempt two to use a list." }),
];

export const AttemptTabs: Story = {
  beforeEach: () => stubPage({ ticket: TWO_ATTEMPTS, comments: ATTEMPT_COMMENTS }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const nav = await canvas.findByRole("navigation", { name: "Attempts" });
    await expect(within(nav).getAllByRole("link")).toHaveLength(3);
    // The ticket's own view holds only the spec conversation.
    await expect(canvas.getByText("The spec, before work started.")).toBeInTheDocument();
    await expect(canvas.queryByText("Told attempt one to use a map.")).toBeNull();

    await userEvent.click(within(nav).getByRole("link", { name: /Attempt 2/ }));
    await expect(await canvas.findByRole("heading", { name: "Attempt 2" })).toBeInTheDocument();
    await expect(canvas.getByText("Told attempt two to use a list.")).toBeInTheDocument();
    await expect(canvas.getByText(/Runs beside the promoted attempt/)).toBeInTheDocument();
  },
};

// The header on an attempt's page: the chip, and the ticket's own fields
// labelled as the ticket's.
export const AttemptPageHeader: Story = {
  args: { initialAttempt: 3 },
  beforeEach: () =>
    stubPage({
      ticket: {
        status: "review",
        pr_url: "https://github.com/o/r/pull/9",
        base_commit: "0123456789abcdef",
        attempts: [
          makeAttempt({ number: 1, status: "in_progress", promoted: true }),
          makeAttempt({ number: 2, status: "in_progress", promoted: false, shadow: true }),
          makeAttempt({ number: 3, status: "review", promoted: false }),
        ],
      },
      comments: ATTEMPT_COMMENTS,
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("attempt 3")).toBeInTheDocument();
    await expect(canvas.getByText("Ticket PR")).toBeInTheDocument();
    await expect(canvas.getByText("Ticket base")).toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Approve attempt 3" })).toBeInTheDocument();
  },
};

// The same ticket's own view, where the ticket's comment form says whom it
// reaches.
export const SeveralAttemptsTicketView: Story = {
  beforeEach: () => stubPage({ ticket: TWO_ATTEMPTS, comments: ATTEMPT_COMMENTS }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByRole("button", { name: "Comment on the ticket (reaches every live attempt)" }),
    ).toBeInTheDocument();
    await expect(canvas.queryByText(/^attempt \d/)).toBeNull();
  },
};

export const BlindThenExpanded: Story = {
  args: { initialAttempt: 2 },
  beforeEach: () => stubPage({ ticket: TWO_ATTEMPTS, comments: ATTEMPT_COMMENTS }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("heading", { name: "Attempt 2" });
    // Blind by default: the sibling's conversation is not on the page.
    await expect(canvas.queryByText("Told attempt one to use a map.")).toBeNull();

    await userEvent.click(canvas.getByRole("button", { name: "Show other attempts' conversations" }));
    await expect(canvas.getByText("Told attempt one to use a map.")).toBeInTheDocument();
    await userEvent.click(canvas.getByRole("button", { name: "Hide other attempts' conversations" }));
    await expect(canvas.queryByText("Told attempt one to use a map.")).toBeNull();
  },
};

export const AddAttemptForm: Story = {
  beforeEach: () =>
    stubPage(
      { ticket: { attempts: [makeAttempt()] } },
      {
        listVariantOptions: async () => [{ id: "v1", name: "fast", version: 1, harness: "pi" }],
        addTicketAttempts: fn(async () => makeTicket({ id: TICKET_ID, attempts: [makeAttempt(), makeAttempt({ number: 2, promoted: false })] })),
      },
    ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/has 1 of 5 live attempts; 4 more/)).toBeInTheDocument();
    await userEvent.click(await canvas.findByRole("button", { name: "Add another" }));
    await expect(canvas.getAllByRole("combobox", { name: /Variant for new attempt/ })).toHaveLength(2);
    await userEvent.click(canvas.getByRole("button", { name: "Add 2 attempts" }));
    await expect(canvas.getByRole("group", { name: "Confirm new attempts" })).toHaveTextContent("loses its promotion");
    await userEvent.click(canvas.getByRole("button", { name: "Confirm: add 2 attempts" }));
    await waitFor(() => expect(apiClient.addTicketAttempts).toHaveBeenCalledWith(TICKET_ID, [null, null]));
    // Two attempts now: the page is split into tabs.
    await expect(await canvas.findByRole("navigation", { name: "Attempts" })).toBeInTheDocument();
  },
};

// "Check now" has never been styled; this story is for the rules around it.
export const WaitingOnATimer: Story = {
  parameters: { defaultStyle: { skip: "the Check now button is unstyled today; a styling change is not part of this story" } },
  beforeEach: () =>
    stubPage({ ticket: { status: "blocked", blocked_until: new Date(Date.now() + 3_600_000).toISOString() } }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/Waiting on a timer/)).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Check now" })).toBeEnabled();
  },
};

export const ProposalWaiting: Story = {
  beforeEach: () =>
    stubPage({}, { listProposals: async () => [makeProposal({ ticket_id: TICKET_ID, agent_name: "console-1" })] }),
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByText(/Proposed by console-1/)).toBeVisible();
  },
};

export const ScopeOverlapsWarned: Story = {
  beforeEach: () =>
    stubPage({
      ticket: {
        status: "open",
        scope_overlaps: [{ ticket_id: 30, title: "Docs overhaul", status: "in_progress", path: "docs/", other: "docs/" }],
      },
    }),
  play: async ({ canvasElement }) => {
    const overlaps = within(await within(canvasElement).findByRole("region", { name: "Scope overlaps" }));
    await expect(overlaps.getByText(/#30 Docs overhaul \(in_progress\): docs\/ overlaps docs\//)).toBeVisible();
  },
};

export const NoAgentCanClaim: Story = {
  beforeEach: () =>
    stubPage({
      ticket: {
        status: "open",
        no_eligible_agent: true,
        required_roles: [{ id: "r1", name: "reviewer" }],
        attempts: [
          makeAttempt({ variant: { name: "fast", version: 2, version_id: "v2", archived: false } }),
        ],
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/No enabled agent is on variant fast v2/)).toBeVisible();
    await expect(canvas.getAllByRole("status").some((e) => e.className.includes("role-warning"))).toBe(true);
  },
};

export const AttemptAwaitingChoice: Story = {
  args: { initialAttempt: 1 },
  beforeEach: () =>
    stubPage({
      ticket: {
        status: "review",
        attempts: [
          makeAttempt({
            number: 1,
            status: "review",
            promoted_at: null,
            worker_cost: [
              { billing_mode: "metered", cost_micros: 1_240_000, input_tokens: 1, output_tokens: 1, requests: 3, unpriced_records: 0 },
            ],
          }),
          makeAttempt({ number: 2, status: "review", promoted: false, promoted_at: null }),
        ],
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("awaiting choice")).toBeVisible();
    await expect(canvas.getByText("Worker cost")).toBeVisible();
  },
};
