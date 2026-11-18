import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { ApiError, type CreateTicketInput } from "@/api";
import { makeAgentRole, makeTicket } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import NewTicketForm from "./new_ticket_form";

const meta = {
  title: "Forms/NewTicketForm",
  component: NewTicketForm,
  args: { onCreated: fn() },
} satisfies Meta<typeof NewTicketForm>;

export default meta;
type Story = StoryObj<typeof meta>;

// Records what the form filed, so a story can check the payload as well as
// what the form drew afterwards.
function stubFiling(filed: CreateTicketInput[]) {
  stubApi({
    listAgentRoles: async () => [makeAgentRole({ id: "r1", name: "reviewer" })],
    getOnboardingStatus: async () => ({ steps: [], code_work_ready: true }),
    createTicket: async (req: CreateTicketInput) => {
      filed.push(req);
      return makeTicket({ id: 42 });
    },
  });
}

export const Collapsed: Story = {
  beforeEach: () => stubApi({ listAgentRoles: async () => [], getOnboardingStatus: async () => ({ steps: [], code_work_ready: true }) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const toggle = canvas.getByRole("button", { name: "File a ticket" });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(canvas.queryByRole("textbox", { name: "Title" })).toBeNull();
    await expect(canvas.queryByRole("button", { name: "Preparing" })).toBeNull();
  },
};

export const Expanded: Story = {
  beforeEach: () => stubFiling([]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "File a ticket" }));
    await expect(await canvas.findByRole("textbox", { name: "Title" })).toBeVisible();
    await expect(canvas.getByRole("textbox", { name: "Body" })).toBeVisible();
    await expect(canvas.getByRole("textbox", { name: "Path scopes (one per line)" })).toBeVisible();
    await expect(canvas.getByRole("checkbox", { name: "Needs discussion" })).not.toBeChecked();
    await expect(canvas.getByRole("checkbox", { name: "Requires human PR approval" })).not.toBeChecked();
    // The picker is a real control of its own: a rule that styles every
    // button in the form once repainted it (e2e theme.spec.ts).
    const priority = canvas.getByRole("combobox", { name: "Priority" });
    await expect(priority).toBeVisible();
    await expect(priority).toHaveTextContent("normal");
    await expect(canvas.getByRole("button", { name: "Preparing" })).toBeEnabled();
    await expect(canvas.getByRole("button", { name: "Open" })).toBeEnabled();
    await expect(await canvas.findByRole("group", { name: "Roles needed to claim this" })).toBeVisible();
  },
};

const filed: CreateTicketInput[] = [];

export const FilesAndResets: Story = {
  beforeEach: () => {
    filed.length = 0;
    stubFiling(filed);
  },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "File a ticket" }));
    await userEvent.type(await canvas.findByRole("textbox", { name: "Title" }), "Add a widget");
    await userEvent.type(canvas.getByRole("textbox", { name: "Body" }), "Details");
    await userEvent.type(canvas.getByRole("textbox", { name: "Path scopes (one per line)" }), "a/b/");
    await userEvent.click(canvas.getByRole("checkbox", { name: "Needs discussion" }));
    await userEvent.click(canvas.getByRole("checkbox", { name: "Requires human PR approval" }));
    await userEvent.click(await canvas.findByRole("checkbox", { name: "reviewer" }));
    await userEvent.click(canvas.getByRole("combobox", { name: "Priority" }));
    await userEvent.click(await within(document.body).findByRole("option", { name: "urgent" }));
    await expect(canvas.getByRole("combobox", { name: "Priority" })).toHaveTextContent("urgent");
    await userEvent.click(canvas.getByRole("button", { name: "Open" }));

    await expect(args.onCreated).toHaveBeenCalledWith(42);
    await expect(filed).toHaveLength(1);
    await expect(filed[0]).toEqual({
      title: "Add a widget",
      body: "Details",
      priority: "urgent",
      status: "open",
      paths: [{ path: "a/b/" }],
      needs_discussion: true,
      requires_approval: true,
      role_ids: ["r1"],
      role_match: "any",
    });
    // The form empties after filing, and stays open for the next one.
    await expect(canvas.getByRole("textbox", { name: "Title" })).toHaveValue("");
    await expect(canvas.getByRole("textbox", { name: "Path scopes (one per line)" })).toHaveValue("");
    await expect(canvas.getByRole("textbox", { name: "Body" })).toHaveValue("");
    await expect(canvas.getByRole("checkbox", { name: "Needs discussion" })).not.toBeChecked();
    await expect(canvas.getByRole("checkbox", { name: "Requires human PR approval" })).not.toBeChecked();
    await expect(canvas.getByRole("checkbox", { name: "reviewer" })).not.toBeChecked();
    await expect(canvas.getByRole("combobox", { name: "Priority" })).toHaveTextContent("normal");
  },
};

export const FilesAsPreparing: Story = {
  beforeEach: () => {
    filed.length = 0;
    stubFiling(filed);
  },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "File a ticket" }));
    await userEvent.type(await canvas.findByRole("textbox", { name: "Title" }), "Draft idea");
    await userEvent.click(canvas.getByRole("button", { name: "Preparing" }));

    await expect(args.onCreated).toHaveBeenCalledWith(42);
    await expect(filed).toHaveLength(1);
    await expect(filed[0]).toMatchObject({ title: "Draft idea", status: "preparing", priority: "normal", paths: [] });
  },
};

export const SubmitError: Story = {
  beforeEach: () =>
    stubApi({
      listAgentRoles: async () => [],
      getOnboardingStatus: async () => ({ steps: [], code_work_ready: true }),
      createTicket: async () => {
        throw new ApiError(409, "paths overlap an in-progress ticket");
      },
    }),
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "File a ticket" }));
    await userEvent.type(await canvas.findByRole("textbox", { name: "Title" }), "Doomed");
    await userEvent.click(canvas.getByRole("button", { name: "Preparing" }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent("paths overlap an in-progress ticket");
    // Nothing is lost: the draft stays for another try, and both buttons come back.
    await expect(canvas.getByRole("textbox", { name: "Title" })).toHaveValue("Doomed");
    await expect(canvas.getByRole("button", { name: "Preparing" })).toBeEnabled();
    await expect(args.onCreated).not.toHaveBeenCalled();
  },
};
