import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect } from "storybook/test";
import type { Agent, SupervisorStatus } from "@/api";
import { makeAgent, makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import MachineRoster from "./machine_roster";

const meta = {
  title: "Workers/MachineRoster",
  component: MachineRoster,
  parameters: { layout: "padded" },
} satisfies Meta<typeof MachineRoster>;

export default meta;
type Story = StoryObj<typeof meta>;

const supervisor = (id: string, name: string, over: Record<string, unknown> = {}) => ({
  id,
  name,
  archived_at: null,
  created_at: "2026-10-01T00:00:00Z",
  disabled_at: null,
  slot_cap: 4,
  slot_count: 2,
  last_seen_at: "2026-11-08T00:00:00Z",
  stale: false,
  ...over,
});

const status = (offline: boolean): SupervisorStatus => ({
  offline,
  claimable: 0,
  last_polled_at: "2026-11-08T00:00:00Z",
  slots: [],
  supervisors: [],
  current_bundle_hash: "",
  current_protocol: 0,
});

const WORKERS: Agent[] = [
  makeAgent({ id: "w1", name: "alpha", supervisor_agent_id: "s1" }),
  makeAgent({ id: "w2", name: "beta", supervisor_agent_id: "s1" }),
  makeAgent({ id: "w3", name: "gamma", supervisor_agent_id: "s2" }),
  makeAgent({ id: "w4", name: "by-hand" }),
];

function roster(workers: Agent[], offline = false): Story["args"] {
  return {
    workers,
    allWorkers: workers,
    status: status(offline),
    renderList: (agents, controlsFor) => (
      <ul>
        {agents.map((a) => (
          <li key={a.id}>
            {a.name}
            {controlsFor?.(a)}
          </li>
        ))}
      </ul>
    ),
  };
}

const stubs = {
  getSetting: async () => ({ name: "provision_from_board", type: "bool", bool_val: true, is_default: false }) as never,
  listProvisionSupervisors: async () => [supervisor("s1", "box")],
  getDesiredState: async () => ({
    revision: 1,
    agents: [
      { id: "a1", kind: "slot", name: "alpha", state: "running", generation: 1, resume_generation: 0, observed: { generation: 1, phase: "running" } },
      { id: "a2", kind: "slot", name: "beta", state: "running", generation: 1, resume_generation: 0, observed: { generation: 1, phase: "failed", reason: "no disk" } },
      { id: "a3", kind: "remote_control", name: "laptop", state: "running", generation: 1, resume_generation: 0, observed: { generation: 0 } },
    ],
  }) as never,
};

const owner = async () => [makeTenant({ is_owner: true })];
const member = async () => [makeTenant({ is_owner: false })];

export const OneSupervisor: Story = {
  args: roster(WORKERS.slice(0, 2)),
  beforeEach: () => stubApi({ ...stubs, listProvisionSupervisors: async () => [supervisor("s1", "box")], listTenants: member }),
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole("heading", { name: "box" })).toBeVisible();
    await expect(canvas.getByText("online")).toBeVisible();
    await expect(canvas.getByText("2 of 4 workers")).toBeVisible();
    await expect(canvas.getByText("Failed: no disk")).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "Change cap for box" })).toBeNull();
  },
};

export const TwoSupervisors: Story = {
  args: roster(WORKERS),
  beforeEach: () =>
    stubApi({ ...stubs, listProvisionSupervisors: async () => [supervisor("s1", "box"), supervisor("s2", "laptop", { slot_count: 1 })], listTenants: owner, listUnmanagedSlots: async () => [] }),
  play: async ({ canvas }) => {
    await expect(await canvas.findByRole("heading", { name: "box" })).toBeVisible();
    await expect(canvas.getByRole("heading", { name: "laptop" })).toBeVisible();
    await expect(canvas.getByRole("heading", { name: "Not on a supervisor" })).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Change cap for laptop" })).toBeVisible();
  },
};

export const Offline: Story = {
  args: roster(WORKERS.slice(0, 2), true),
  beforeEach: () => stubApi({ ...stubs, listProvisionSupervisors: async () => [supervisor("s1", "box")], listTenants: member }),
  play: async ({ canvas }) => {
    await expect(await canvas.findByText("offline")).toBeVisible();
  },
};

export const OverCap: Story = {
  args: roster(WORKERS.slice(0, 2)),
  beforeEach: () => stubApi({ ...stubs, listProvisionSupervisors: async () => [supervisor("s1", "box", { slot_count: 5, slot_cap: 2 })], listTenants: member }),
  play: async ({ canvas }) => {
    await expect(await canvas.findByText("5 of 2 workers")).toBeVisible();
    await expect(canvas.getByRole("note")).toHaveTextContent(/more workers than its cap/);
  },
};

export const NoSupervisorYet: Story = {
  args: roster(WORKERS.slice(3)),
  beforeEach: () => stubApi({ ...stubs, listProvisionSupervisors: async () => [], listTenants: member }),
  play: async ({ canvas }) => {
    await expect(await canvas.findByText(/No machine runs workers yet/)).toBeVisible();
    await expect(canvas.getByText("by-hand")).toBeVisible();
  },
};

export const NotManaged: Story = {
  args: roster([]),
  beforeEach: () =>
    stubApi({
      ...stubs,
      listProvisionSupervisors: async () => [supervisor("s1", "box")],
      listTenants: owner,
      listUnmanagedSlots: async () => [
        { kind: "slot", name: "orphan", phase: "running", offline: false },
        { kind: "slot", name: "stale", phase: "failed", offline: true },
      ],
    }),
  play: async ({ canvas }) => {
    await expect(await canvas.findByText("Not managed by the board")).toBeVisible();
    await expect(canvas.getByText("failed (offline)")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Import all" })).toBeVisible();
  },
};
