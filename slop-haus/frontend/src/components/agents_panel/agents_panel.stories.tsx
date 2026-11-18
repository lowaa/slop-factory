import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ApiError, type Agent } from "@/api";
import { makeAgent, makeAgentRole, makeHarnessConfig } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import AgentsPanel from "./agents_panel";

const ROSTER = [
  makeAgent({ id: "a1", name: "alpha" }),
  makeAgent({ id: "a2", name: "bravo", disabled_at: "2026-01-02T00:00:00Z" }),
  makeAgent({ id: "a3", name: "charlie", kind: "console" }),
];

function stubRoster(agents: Agent[], extra: Partial<Parameters<typeof stubApi>[0]> = {}) {
  return () =>
    stubApi({
      listAgents: async () => agents,
      getSetting: async () => ({ name: "provision_from_board", type: "bool", bool_val: false }) as never,
      listGrants: async () => [],
      listAgentRoles: async () => [makeAgentRole()],
      getHarnessConfig: async () => makeHarnessConfig(),
      listAgentMessages: async () => [],
      getSupervisorStatus: async () => ({ offline: false, claimable: 0, last_polled_at: null, slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 }),
      listVariants: async () => {
        throw new ApiError(403, "owners only");
      },
      streamBoardChanges: () => () => {},
      ...extra,
    });
}

const meta = {
  title: "Workers/AgentsPanel",
  component: AgentsPanel,
  parameters: { layout: "padded" },
} satisfies Meta<typeof AgentsPanel>;

export default meta;
type Story = StoryObj<typeof meta>;

export const EmptyRoster: Story = {
  beforeEach: stubRoster([]),
  play: async ({ canvas }) => {
    await expect(await canvas.findByText("No workers yet.")).toBeVisible();
  },
};

export const MixedRoster: Story = {
  beforeEach: stubRoster(ROSTER),
  play: async ({ canvas }) => {
    await expect(await canvas.findByText("bravo")).toBeVisible();
    await expect(canvas.getByText("alpha")).toBeVisible();
    await expect(canvas.getByRole("checkbox", { name: "Hide disabled" })).not.toBeChecked();
  },
};

export const SessionsSection: Story = {
  beforeEach: stubRoster(ROSTER),
  play: async ({ canvas }) => {
    const sessions = await canvas.findByRole("region", { name: "Your sessions" });
    await expect(within(sessions).getByText("charlie")).toBeVisible();
    await expect(within(sessions).queryByText("alpha")).toBeNull();
  },
};

export const HideDisabledOn: Story = {
  beforeEach: stubRoster(ROSTER),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("bravo");
    await userEvent.click(canvas.getByRole("checkbox", { name: "Hide disabled" }));
    await expect(canvas.queryByText("bravo")).toBeNull();
    await expect(canvas.getByText("alpha")).toBeVisible();
    await expect(canvas.queryByText("No workers yet.")).toBeNull();
  },
};

const LONG_ROSTER = Array.from({ length: 15 }, (_, i) => makeAgent({ id: `a${i}`, name: `worker-${i}` }));

// The last row's button is far below the top of the page: the outcome of
// the click has to appear beside it.
export const RefusalOnLongRoster: Story = {
  beforeEach: stubRoster(LONG_ROSTER, {
    setAgentDisabled: async () => {
      throw new ApiError(409, "agent is busy");
    },
  }),
  play: async ({ canvas }) => {
    const row = (await canvas.findByText("worker-14")).closest("li") as HTMLElement;
    await userEvent.click(within(row).getByRole("button", { name: "Disable" }));
    await expect(await within(row).findByRole("alert")).toHaveTextContent("agent is busy");
  },
};

export const SupervisorOffline: Story = {
  beforeEach: stubRoster(ROSTER, {
    getSupervisorStatus: async () => ({ offline: true, claimable: 2, last_polled_at: "2026-10-19T10:00:00Z", slots: [], supervisors: [], current_bundle_hash: "", current_protocol: 0 }),
  }),
  play: async ({ canvas }) => {
    await expect(await canvas.findByText(/Supervisor offline: last polled/)).toBeVisible();
  },
};
