import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { ApiError, type Ticket, type TicketAttempt } from "@/api";
import { makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import BodyEditor from "./body_editor";
import { useBodyEditor } from "./use_body_editor";
import { useTicketStore } from "./use_ticket_store";
import "./ticket_detail.scss";

const TICKET_ID = 7;

// The editor's state lives in the page that renders it, so the story drives
// the real store and hook rather than faking the actions.
function Host() {
  const store = useTicketStore(TICKET_ID);
  const editor = useBodyEditor(store, TICKET_ID);
  if (store.state.status !== "loaded") return <p>Loading…</p>;
  return (
    <>
      <h2>{store.state.ticket.title}</h2>
      <BodyEditor ticket={store.state.ticket} editor={editor} />
    </>
  );
}

const meta = {
  title: "Ticket detail/BodyEditor",
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

const EDIT = "Edit title and description";

interface Sent {
  title?: string;
  body?: string;
  base_revision: number;
}
const sent: Sent[] = [];

// Stateful: the server keeps the saved text, so a save is visible on the
// next read as well as in the reply.
function stubTicket(initial: Ticket, editTicket?: (input: Sent) => Promise<Ticket>) {
  let current = initial;
  sent.length = 0;
  stubApi({
    streamBoardChanges: () => () => {},
    getTicket: async () => current,
    listComments: async () => [],
    listAgentContexts: async () => [],
    listTicketAttachments: async () => [],
    listRevisions: async () => [],
    listAttemptReviewRuns: async () => [],
    listStatusHistory: async () => [],
    editTicket: async (_id, input) => {
      const body = input as Sent;
      sent.push(body);
      if (editTicket) return editTicket(body);
      current = { ...current, ...body, revision: current.revision + 1 };
      return current;
    },
  });
}

const TICKET = makeTicket({ id: TICKET_ID, title: "Delete button", body: "first draft", revision: 2 });

export const Viewing: Story = {
  beforeEach: () => stubTicket(TICKET),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("first draft")).toBeVisible();
    await expect(canvas.getByRole("button", { name: EDIT })).toBeEnabled();
  },
};

export const NoDescription: Story = {
  beforeEach: () => stubTicket({ ...TICKET, body: "" }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("No description yet.")).toBeVisible();
  },
};

export const Editing: Story = {
  beforeEach: () => stubTicket(TICKET),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await expect(canvas.getByLabelText("Title")).toHaveValue("Delete button");
    await expect(canvas.getByLabelText("Description")).toHaveValue("first draft");
    await expect(canvas.getByRole("button", { name: "Save" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Cancel" })).toBeEnabled();
  },
};

export const Saving: Story = {
  beforeEach: () => stubTicket(TICKET, () => new Promise<Ticket>(() => {})),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await userEvent.type(canvas.getByLabelText("Title"), "!");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(await canvas.findByRole("button", { name: "Saving…" })).toBeDisabled();
    await expect(canvas.getByRole("button", { name: "Cancel" })).toBeDisabled();
  },
};

export const SaveEdit: Story = {
  beforeEach: () => stubTicket(TICKET),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await userEvent.clear(canvas.getByLabelText("Title"));
    await userEvent.type(canvas.getByLabelText("Title"), "Reinstall script");
    await userEvent.clear(canvas.getByLabelText("Description"));
    await userEvent.type(canvas.getByLabelText("Description"), "second draft");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));

    // Back to the view, showing the saved text.
    await expect(await canvas.findByText("second draft")).toBeVisible();
    await expect(canvas.getByRole("heading", { name: "Reinstall script" })).toBeVisible();
    await expect(canvas.queryByLabelText("Description")).toBeNull();
    // Against the revision the editor was opened on.
    await expect(sent).toEqual([{ title: "Reinstall script", body: "second draft", base_revision: 2 }]);
  },
};

export const OnlyChangedFieldsSent: Story = {
  beforeEach: () => stubTicket(TICKET),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await userEvent.type(canvas.getByLabelText("Description"), " plus more");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(sent).toHaveLength(1));
    await expect(sent[0]).toEqual({ body: "first draft plus more", base_revision: 2 });
  },
};

export const Cancel: Story = {
  beforeEach: () => stubTicket(TICKET),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await userEvent.type(canvas.getByLabelText("Description"), " scribble");
    await userEvent.click(canvas.getByRole("button", { name: "Cancel" }));
    await expect(await canvas.findByText("first draft")).toBeVisible();
    await expect(canvas.queryByLabelText("Description")).toBeNull();
    await expect(sent).toEqual([]);
  },
};

export const EmptyTitle: Story = {
  beforeEach: () => stubTicket(TICKET),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await userEvent.clear(canvas.getByLabelText("Title"));
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("The title can't be empty.");
    await expect(sent).toEqual([]);
  },
};

export const SaveError: Story = {
  beforeEach: () =>
    stubTicket(TICKET, async () => {
      throw new ApiError(500, "database unavailable");
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await userEvent.type(canvas.getByLabelText("Description"), " more");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent("database unavailable");
    // The draft is the only copy of what was typed, so the editor stays open on it.
    await expect(canvas.getByLabelText("Description")).toHaveValue("first draft more");
    await expect(canvas.getByRole("button", { name: "Save" })).toBeEnabled();
    await expect(sent).toEqual([{ body: "first draft more", base_revision: 2 }]);
  },
};

export const NothingChanged: Story = {
  beforeEach: () => stubTicket(TICKET),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    // Saving with nothing changed just closes the editor.
    await expect(await canvas.findByRole("button", { name: EDIT })).toBeEnabled();
    await expect(sent).toEqual([]);
  },
};

export const Conflict: Story = {
  beforeEach: () => {
    const theirs = { ...TICKET, body: "their edit", revision: 3 };
    let refreshed = false;
    stubTicket(TICKET, async () => {
      throw new ApiError(409, "revision conflict");
    });
    stubApi({
    streamBoardChanges: () => () => {},
      getTicket: async () => (refreshed ? theirs : TICKET),
      listRevisions: async () => {
        refreshed = true;
        return [];
      },
    });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await userEvent.type(canvas.getByLabelText("Description"), " more");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("Someone else edited this ticket");
    // The draft stays on its stale revision.
    await expect(canvas.getByLabelText("Description")).toHaveValue("first draft more");
    await expect(sent).toEqual([{ body: "first draft more", base_revision: 2 }]);

    // Cancelling shows their edit, fetched after the refusal, not the stale text.
    await userEvent.click(canvas.getByRole("button", { name: "Cancel" }));
    await expect(await canvas.findByText("their edit")).toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: EDIT }));
    await expect(canvas.getByLabelText("Description")).toHaveValue("their edit");
  },
};

const TWO_ATTEMPTS: TicketAttempt[] = [
  {
    number: 1, status: "in_progress", promoted: true, promoted_at: null, promoted_by_user_id: null, pr_url: null,
    approved_at: null, approved_by_user_id: null, changes_requested_at: null, review_started_at: null,
    started_revision: 1, shadow: false, holder: null, variant: null, variant_settable: false, worker_cost: [], runs: [], signals: null, verdict: null, discussion: null,
  },
  {
    number: 2, status: "in_progress", promoted: false, promoted_at: null, promoted_by_user_id: null, pr_url: null,
    approved_at: null, approved_by_user_id: null, changes_requested_at: null, review_started_at: null,
    started_revision: 2, shadow: true, holder: null, variant: null, variant_settable: false, worker_cost: [], runs: [], signals: null, verdict: null, discussion: null,
  },
];

export const WarnsWhileAttemptsRun: Story = {
  beforeEach: () => stubTicket({ ...TICKET, attempts: TWO_ATTEMPTS }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await userEvent.type(canvas.getByLabelText("Title"), "!");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(await canvas.findByRole("note")).toHaveTextContent(
      "Attempt 1 started from revision 1, attempt 2 from revision 2. Editing changes the spec under them.",
    );
    await expect(sent).toHaveLength(0);
    await userEvent.click(canvas.getByRole("button", { name: "Save anyway" }));
    await waitFor(() => expect(sent).toHaveLength(1));
  },
};
