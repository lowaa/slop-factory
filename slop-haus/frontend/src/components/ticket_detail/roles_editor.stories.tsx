import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ApiError, type AgentRole, type RoleMatch, type Ticket } from "@/api";
import { makeAgentRole, makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import RolesEditor from "./roles_editor";
import { useRolesEditor, useTenantRoles } from "./use_roles_editor";
import { useTicketStore } from "./use_ticket_store";
import "./ticket_detail.scss";

const TICKET_ID = 7;

// The editor's state lives in the page that renders it, so the story drives
// the real store and hooks rather than faking the actions.
function Host() {
  const store = useTicketStore(TICKET_ID);
  const tenantRoles = useTenantRoles();
  const editor = useRolesEditor(store, TICKET_ID);
  if (store.state.status !== "loaded") return <p>Loading…</p>;
  return (
    <RolesEditor
      ticket={store.state.ticket}
      tenantRoles={tenantRoles}
      editor={editor}
    />
  );
}

const meta = {
  title: "Ticket detail/RolesEditor",
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

const DEPLOY = makeAgentRole({
  id: "11111111-1111-1111-1111-111111111111",
  name: "deploy",
});
const REVIEWER = makeAgentRole({
  id: "22222222-2222-2222-2222-222222222222",
  name: "reviewer",
});
const EDIT = "Edit roles";

interface SentRoles {
  ticketId: number;
  roleIds: string[];
  roleMatch?: RoleMatch;
}
const sent: SentRoles[] = [];

function stubTicket(
  ticket: Ticket,
  opts: {
    roles?: AgentRole[];
    setRoles?: (input: SentRoles) => Promise<Ticket>;
  } = {},
) {
  sent.length = 0;
  stubApi({
    streamBoardChanges: () => () => {},
    getTicket: async () => ticket,
    listComments: async () => [],
    listAgentContexts: async () => [],
    listTicketAttachments: async () => [],
    listRevisions: async () => [],
    listAttemptReviewRuns: async () => [],
    listStatusHistory: async () => [],
    listAgentRoles: async () => opts.roles ?? [DEPLOY, REVIEWER],
    listVariantOptions: async () => [],
    setTicketRoles: async (ticketId, roleIds, roleMatch) => {
      const input = { ticketId, roleIds, roleMatch };
      sent.push(input);
      if (opts.setRoles) return opts.setRoles(input);
      return {
        ...ticket,
        required_roles: roleIds.map((id) => ({
          id,
          name: id === DEPLOY.id ? "deploy" : "reviewer",
        })),
        role_match: roleMatch ?? ticket.role_match,
      };
    },
  });
}

const TICKET = makeTicket({
  id: TICKET_ID,
  required_roles: [{ id: DEPLOY.id, name: "deploy" }],
});

export const Viewing: Story = {
  beforeEach: () => stubTicket(TICKET),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("role: deploy")).toBeVisible();
    await expect(canvas.getByRole("button", { name: EDIT })).toBeEnabled();
  },
};

export const Editing: Story = {
  beforeEach: () => stubTicket(TICKET),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    const group = await canvas.findByRole("group", {
      name: "Roles needed to claim this",
    });
    await expect(
      within(group).getByRole("checkbox", { name: "deploy" }),
    ).toBeChecked();
    await expect(
      within(group).getByRole("checkbox", { name: "reviewer" }),
    ).not.toBeChecked();
    await expect(canvas.getByRole("button", { name: "Save" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Cancel" })).toBeEnabled();
  },
};

export const SaveRoles: Story = {
  beforeEach: () => stubTicket(TICKET),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await userEvent.click(
      await canvas.findByRole("checkbox", { name: "reviewer" }),
    );
    await userEvent.click(canvas.getByRole("radio", { name: "all of these" }));
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));

    await expect(
      await canvas.findByRole("button", { name: EDIT }),
    ).toBeEnabled();
    await expect(
      canvas.queryByRole("group", { name: "Roles needed to claim this" }),
    ).toBeNull();
    await expect(sent).toEqual([
      {
        ticketId: TICKET_ID,
        roleIds: [DEPLOY.id, REVIEWER.id],
        roleMatch: "all",
      },
    ]);
    await expect(canvas.getByText(/deploy/)).toBeVisible();
    await expect(canvas.getByText(/reviewer/)).toBeVisible();
  },
};

export const SaveError: Story = {
  beforeEach: () =>
    stubTicket(TICKET, {
      setRoles: async () => {
        throw new ApiError(500, "database unavailable");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: EDIT }));
    await userEvent.click(
      await canvas.findByRole("checkbox", { name: "reviewer" }),
    );
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "database unavailable",
    );
    // The choice is kept so the save can be retried.
    await expect(
      canvas.getByRole("checkbox", { name: "reviewer" }),
    ).toBeChecked();
    await expect(canvas.getByRole("button", { name: "Save" })).toBeEnabled();
  },
};
