import { useState } from "react";
import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import type { RoleMatch } from "@/api";
import { makeAgentRole } from "@/test/fixtures";
import RoleRequirementPicker from "./role_requirement_picker";

const ROLES = [
  makeAgentRole({ id: "r1", name: "reviewer" }),
  makeAgentRole({ id: "r2", name: "deployer" }),
  makeAgentRole({ id: "r3", name: "closer" }),
];

interface HostProps {
  initialSelected: string[];
  initialMatch: RoleMatch;
  roles: typeof ROLES;
  emptyHint?: string;
}

// The callers own the selection; holding it here keeps the controls reacting
// to clicks, which fixed props would not.
function Host({ initialSelected, initialMatch, roles, emptyHint }: HostProps) {
  const [selected, setSelected] = useState(initialSelected);
  const [match, setMatch] = useState<RoleMatch>(initialMatch);
  return (
    <RoleRequirementPicker
      roles={roles}
      selectedIds={selected}
      onSelectionChange={setSelected}
      matchMode={match}
      onMatchModeChange={setMatch}
      legend="Roles needed"
      emptyHint={emptyHint}
    />
  );
}

const meta = {
  title: "Tickets/RoleRequirementPicker",
  component: Host,
  args: { initialSelected: [], initialMatch: "any", roles: ROLES },
} satisfies Meta<typeof Host>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NoRoles: Story = {
  args: { roles: [], emptyHint: "No roles yet." },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("No roles yet.")).toBeVisible();
    await expect(canvas.queryByRole("checkbox")).toBeNull();
  },
};

export const NoneTicked: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getAllByRole("checkbox")).toHaveLength(3);
    await expect(canvas.queryByRole("radio")).toBeNull();
    // Picking a second role is what reveals the any/all toggle.
    await userEvent.click(canvas.getByRole("checkbox", { name: "reviewer" }));
    await expect(canvas.queryByRole("radio")).toBeNull();
    await userEvent.click(canvas.getByRole("checkbox", { name: "closer" }));
    await expect(canvas.getByRole("radio", { name: "any of these" })).toBeChecked();
  },
};

export const SomeTicked: Story = {
  args: { initialSelected: ["r2"] },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("checkbox", { name: "deployer" })).toBeChecked();
    await expect(canvas.getByRole("checkbox", { name: "reviewer" })).not.toBeChecked();
    await userEvent.click(canvas.getByRole("checkbox", { name: "deployer" }));
    await expect(canvas.getByRole("checkbox", { name: "deployer" })).not.toBeChecked();
  },
};

export const MatchAny: Story = {
  args: { initialSelected: ["r1", "r2"], initialMatch: "any" },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("radio", { name: "any of these" })).toBeChecked();
    await expect(canvas.getByRole("radio", { name: "all of these" })).not.toBeChecked();
  },
};

export const MatchAll: Story = {
  args: { initialSelected: ["r1", "r2"], initialMatch: "all" },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("radio", { name: "all of these" })).toBeChecked();
    await userEvent.click(canvas.getByRole("radio", { name: "any of these" }));
    await expect(canvas.getByRole("radio", { name: "any of these" })).toBeChecked();
    await expect(canvas.getByRole("radio", { name: "all of these" })).not.toBeChecked();
  },
};
