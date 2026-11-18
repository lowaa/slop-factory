import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent } from "storybook/test";
import { ApiError, type Conductor } from "@/api";
import { makeConductor, makeHarnessConfig } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import { AgentManagementNotes, AgentMenu } from "./agent_management";
import { useAgentManagement } from "./use_agent_management";

function Management({ conductor }: { conductor: Conductor }) {
  const management = useAgentManagement(conductor, () => {});
  return (
    <div style={{ width: 260 }}>
      <div style={{ display: "flex", justifyContent: "space-between" }}>
        <strong>{conductor.name}</strong>
        <AgentMenu management={management} />
      </div>
      <AgentManagementNotes management={management} />
    </div>
  );
}

const meta = {
  title: "RemoteControl/AgentManagement",
  component: Management,
  args: { conductor: makeConductor({ id: "c-1", name: "laptop" }) },
} satisfies Meta<typeof Management>;

export default meta;
type Story = StoryObj<typeof meta>;

export const MenuItems: Story = {
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Manage laptop" }));
    for (const name of ["Edit config", "New setup script", "Disable", "Remove"]) {
      await expect(canvas.getByRole("button", { name })).toBeVisible();
    }
  },
};

export const ConfirmRemove: Story = {
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Manage laptop" }));
    await userEvent.click(canvas.getByRole("button", { name: "Remove" }));
    await expect(canvas.getByRole("group", { name: "Confirm removing laptop" })).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "Disable" })).not.toBeInTheDocument();
  },
};

export const ConfirmNewSetupScript: Story = {
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Manage laptop" }));
    await userEvent.click(canvas.getByRole("button", { name: "New setup script" }));
    await expect(canvas.getByRole("group", { name: "Confirm new setup script for laptop" })).toBeVisible();
  },
};

export const EditConfig: Story = {
  beforeEach: () => stubApi({ getHarnessConfig: async () => makeHarnessConfig() }),
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Manage laptop" }));
    await userEvent.click(canvas.getByRole("button", { name: "Edit config" }));
    await expect(await canvas.findByRole("form", { name: "Harness config for laptop" })).toBeVisible();
  },
};

export const ConfigLoadFails: Story = {
  beforeEach: () =>
    stubApi({
      getHarnessConfig: async () => {
        throw new ApiError(500, "config is down");
      },
    }),
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Manage laptop" }));
    await userEvent.click(canvas.getByRole("button", { name: "Edit config" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("config is down");
  },
};

export const Disabled: Story = {
  args: { conductor: makeConductor({ id: "c-1", name: "laptop", disabled_at: "2026-01-02T00:00:00Z" }) },
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Manage laptop" }));
    await expect(canvas.getByRole("button", { name: "Enable" })).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "New setup script" })).not.toBeInTheDocument();
  },
};
