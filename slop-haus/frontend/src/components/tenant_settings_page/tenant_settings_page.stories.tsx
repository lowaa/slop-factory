import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { apiClient, ApiError } from "@/api";
import { makeAgent, makeHarnessConfig, makeServerSettingChange, makeSetting, makeTenant, makeTenantUserSetting } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import TenantSettingsPage from "./tenant_settings_page";

const autoApprove = [
  "approvals.auto.status",
  "approvals.auto.edit",
  "approvals.auto.ready",
  "approvals.auto.review_decision",
  "approvals.auto.discussion_answer",
].map((name) => makeTenantUserSetting({ name }));

const pausedChanges = [
  makeServerSettingChange({ id: 2 }),
  makeServerSettingChange({
    id: 1,
    changed_by_id: null,
    changed_by_name: null,
    before_history_kept: true,
    from: makeServerSettingChange().to,
    to: makeServerSettingChange().from,
  }),
];

function stubTenant(overrides: Parameters<typeof stubApi>[0] = {}) {
  return stubApi({
    listSettings: async () => [makeSetting({ bool_val: false, is_default: false })],
    getSettingHistory: async () => ({ changes: pausedChanges }),
    listMyTenantSettings: async () => autoApprove,
    listMySettings: async () => [],
    listTenants: async () => [makeTenant({ is_owner: true })],
    getStewardDigest: async () => ({ categories: [], decisions: [], decisions_cut: false, total: { assessed: 0, needs_human: 0, awaiting: 0, unclassified: 0, agreed: 0, disagreed: 0 } }) as never,
    getWorkspace: async () => ({ manifest: null }) as never,
    getHarnessConfig: async () => makeHarnessConfig(),
    listAgents: async () => [makeAgent({ kind: "steward" })],
    getFleetInstructions: async () =>
      ({ default_id: "w1", versions: [{ id: "w1", body: "salary" }] }) as never,
    ...overrides,
  });
}

const meta = {
  title: "Settings/TenantSettingsPage",
  component: TenantSettingsPage,
} satisfies Meta<typeof TenantSettingsPage>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Desktop: Story = {
  beforeEach: () => stubTenant(),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const tenant = within(await canvas.findByRole("region", { name: "This tenant" }));
    await expect(await tenant.findByRole("switch", { name: "Ticket processing" })).not.toBeChecked();
    const mine = within(await canvas.findByRole("region", { name: "You in this tenant" }));
    await expect(mine.getAllByRole("switch")).toHaveLength(6);
  },
};

export const Phone: Story = {
  ...Desktop,
  globals: { viewport: { value: "mobile1", isRotated: false } },
};

export const DefaultValue: Story = {
  beforeEach: () => stubTenant({ listSettings: async () => [makeSetting()] }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("Default value");
    await expect(canvas.queryByText(/last changed by/)).toBeNull();
  },
};

const provision = (on: boolean) => makeSetting({ name: "provision_from_board", bool_val: on, is_default: false });

export const ProvisionOff: Story = {
  beforeEach: () => stubTenant({ listSettings: async () => [provision(false)] }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("switch", { name: "Manage agents from the board" }));
    await expect(await canvas.findByRole("alertdialog")).toHaveTextContent("adopted as they are");
  },
};

export const ProvisionOn: Story = {
  beforeEach: () => stubTenant({ listSettings: async () => [provision(true)] }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("switch", { name: "Manage agents from the board" }));
    await expect(await canvas.findByRole("alertdialog")).toHaveTextContent("Nothing stops");
  },
};

export const ProvisionNonOwner: Story = {
  beforeEach: () =>
    stubTenant({
      listSettings: async () => [provision(true)],
      listTenants: async () => [makeTenant({ is_owner: false })],
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByRole("switch", { name: "Manage agents from the board" })).toBeDisabled();
    await expect(canvas.getByText("Only an owner can change this.")).toBeVisible();
  },
};

export const HistoryOpen: Story = {
  beforeEach: () => stubTenant(),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "History of Ticket processing" }));
    const changes = within(await canvas.findByRole("list", { name: "Changes" })).getAllByRole("listitem");
    await expect(changes).toHaveLength(2);
    await expect(changes[0]).toHaveTextContent("on → off");
    await expect(changes[1]).toHaveTextContent("before history was kept");
  },
};

export const HistoryOpenPhone: Story = {
  ...HistoryOpen,
  globals: { viewport: { value: "mobile1", isRotated: false } },
};

export const WriteError: Story = {
  beforeEach: () =>
    stubTenant({
      setMyTenantBoolSetting: fn(async () => {
        throw new ApiError(500, "Could not save.");
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const toggle = await canvas.findByRole("switch", { name: "Parking or cancelling a ticket" });
    await userEvent.click(toggle);
    await expect(await canvas.findByRole("alert")).toHaveTextContent("Could not save.");
    await expect(apiClient.setMyTenantBoolSetting).toHaveBeenCalledWith("approvals.auto.status", true);
    await expect(toggle).not.toBeChecked();
  },
};

// A setting that is not a switch shows its value instead.
export const NonSwitchSetting: Story = {
  beforeEach: () =>
    stubTenant({
      listSettings: async () => [makeSetting({ name: "ticket_page_size", type: "int", int_val: 25, bool_val: null })],
      listMyTenantSettings: async () => [
        ...autoApprove,
        makeTenantUserSetting({ name: "digest.hour", type: "int", int_val: 9, bool_val: null }),
      ],
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("25")).toBeVisible();
    await expect(canvas.getByText("9")).toBeVisible();
  },
};

export const YourSettingHistory: Story = {
  beforeEach: () =>
    stubTenant({
      getMyTenantSettingHistory: async () => ({
        changes: [
          { id: 2, type: "bool", bool_val: true, int_val: null, float_val: null, str_val: null, set_at: "2026-10-12T08:00:00Z", earlier_membership: false },
          { id: 1, type: "bool", bool_val: false, int_val: null, float_val: null, str_val: null, set_at: "2026-10-11T08:00:00Z", earlier_membership: false },
        ],
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const yours = within(await canvas.findByRole("group", { name: "Auto-approve your sessions' proposals" }));
    await userEvent.click(yours.getAllByRole("button", { name: /^History of / })[0]);
    const changes = await canvas.findByRole("list", { name: "Changes" });
    await expect(within(changes).getAllByRole("listitem")[0]).toHaveTextContent("off → on");
  },
};

export const HistoryFails: Story = {
  beforeEach: () =>
    stubTenant({
      getSettingHistory: async () => {
        throw new ApiError(500, "History is unavailable.");
      },
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "History of Ticket processing" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("History is unavailable.");
  },
};

// The owner's Steward section: the level is shown, and a refused tier save is
// shown beside the tiers.
export const StewardOwner: Story = {
  beforeEach: () =>
    stubTenant({
      listSettings: async () => [
        makeSetting(),
        makeSetting({ name: "steward_level", type: "int", int_val: 3, bool_val: null, is_default: false }),
        makeSetting({ name: "steward_veto_minutes", type: "int", int_val: 60, bool_val: null }),
        makeSetting({ name: "steward_daily_cap", type: "int", int_val: 50, bool_val: null }),
        makeSetting({ name: "steward_category_tiers", type: "string", string_val: "{}", bool_val: null }),
      ],
      updateSetting: fn(async () => {
        throw new ApiError(422, "steward: priority may not be changed");
      }),
    }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const steward = within(await canvas.findByRole("region", { name: "Steward" }));
    await expect(steward.getByRole("radio", { name: /3 Routine/ })).toBeChecked();
    await userEvent.click(steward.getByRole("combobox", { name: "Change a ticket's priority" }));
    await userEvent.click(await within(document.body).findByRole("option", { name: "human" }));
    await expect(await steward.findByRole("alert")).toHaveTextContent("priority may not be changed");
    await expect(canvas.queryByText(/steward_/)).toBeNull();
  },
};

export const StewardMemberSeesNothing: Story = {
  beforeEach: () => stubTenant({ listTenants: async () => [makeTenant({ is_owner: false })] }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByRole("region", { name: "This tenant" });
    await expect(canvas.queryByRole("region", { name: "Steward" })).toBeNull();
  },
};
