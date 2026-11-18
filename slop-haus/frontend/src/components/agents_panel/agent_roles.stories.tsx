import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { RoleInUseError } from "@/api";
import { makeAgentRole } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import AgentRoles from "./agent_roles";
import "./agents_panel.scss";

const roles = [
  makeAgentRole({ id: "r1", name: "reviewer", description: "Reviews pull requests." }),
  makeAgentRole({ id: "r2", name: "frontend", description: "Works on the UI." }),
];

const meta = {
  title: "Agents/AgentRoles",
  component: AgentRoles,
  args: { state: { status: "loaded", roles }, onChanged: fn() },
} satisfies Meta<typeof AgentRoles>;

export default meta;
type Story = StoryObj<typeof meta>;

const createAgentRole = fn(async (name: string, description: string) => makeAgentRole({ name, description }));
const updateAgentRole = fn(async (id: string, _patch: { name?: string; description?: string }) =>
  makeAgentRole({ id }),
);
const deleteAgentRole = fn(async (_id: string, _clearTickets?: boolean) => {});

export const RoleList: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("reviewer")).toBeInTheDocument();
    await expect(canvas.getByText("Works on the UI.")).toBeInTheDocument();
    await expect(canvas.getAllByRole("button", { name: "Edit" })).toHaveLength(2);
  },
};

export const Empty: Story = {
  args: { state: { status: "loaded", roles: [] } },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).getByText("No roles yet.")).toBeInTheDocument();
  },
};

export const AddingRole: Story = {
  beforeEach: () => {
    createAgentRole.mockClear();
    stubApi({ createAgentRole });
  },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByLabelText("Role name"), "tester");
    await userEvent.type(canvas.getByLabelText("Description (optional)"), "Writes tests.");
    await userEvent.click(canvas.getByRole("button", { name: "Add role" }));
    await expect(createAgentRole).toHaveBeenCalledWith("tester", "Writes tests.");
    await expect(args.onChanged).toHaveBeenCalledTimes(1);
  },
};

export const EditingRole: Story = {
  beforeEach: () => {
    updateAgentRole.mockClear();
    stubApi({ updateAgentRole });
  },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click((await canvas.findAllByRole("button", { name: "Edit" }))[0]);
    const name = canvas.getByLabelText("Name");
    await expect(name).toHaveValue("reviewer");
    await userEvent.clear(name);
    await userEvent.type(name, "approver");
    await userEvent.click(canvas.getByRole("button", { name: "Save" }));
    await expect(updateAgentRole).toHaveBeenCalledWith("r1", { name: "approver", description: "Reviews pull requests." });
    await expect(args.onChanged).toHaveBeenCalledTimes(1);
  },
};

export const NameRequired: Story = {
  beforeEach: () => {
    createAgentRole.mockClear();
    stubApi({ createAgentRole });
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Add role" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("Give the role a name first.");
    await expect(createAgentRole).not.toHaveBeenCalled();
  },
};

export const DeletingRole: Story = {
  beforeEach: () => {
    deleteAgentRole.mockClear();
    stubApi({ deleteAgentRole });
  },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click((await canvas.findAllByRole("button", { name: "Delete" }))[0]);
    const confirm = canvas.getByRole("group", { name: "Confirm delete role reviewer" });
    await userEvent.click(within(confirm).getByRole("button", { name: "Delete" }));
    await expect(deleteAgentRole).toHaveBeenCalledWith("r1", false);
    await expect(args.onChanged).toHaveBeenCalledTimes(1);
  },
};

export const DeleteConflict: Story = {
  beforeEach: () => {
    deleteAgentRole.mockClear();
    deleteAgentRole.mockRejectedValueOnce(
      new RoleInUseError("role in use", [
        { id: 7, title: "Fix the board", status: "open" },
        { id: 9, title: "Add a story", status: "open" },
      ]),
    );
    stubApi({ deleteAgentRole });
  },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await userEvent.click((await canvas.findAllByRole("button", { name: "Delete" }))[0]);
    const confirm = canvas.getByRole("group", { name: "Confirm delete role reviewer" });
    await userEvent.click(within(confirm).getByRole("button", { name: "Delete" }));
    await expect(await canvas.findByText(/#7 Fix the board/)).toBeInTheDocument();
    await expect(canvas.getByText(/#9 Add a story/)).toBeInTheDocument();
    await expect(args.onChanged).not.toHaveBeenCalled();
    await userEvent.click(canvas.getByRole("button", { name: "Remove from 2 tickets and delete" }));
    await expect(deleteAgentRole).toHaveBeenLastCalledWith("r1", true);
    await expect(args.onChanged).toHaveBeenCalledTimes(1);
  },
};
