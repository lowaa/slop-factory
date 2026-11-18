import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent } from "storybook/test";
import { makeAgent, makeAgentRole, makeAgentSession, makeLease, makeHarnessConfig, makeHarnessConfigScope, makeVariantVersion } from "@/test/fixtures";
import { chooseOption } from "../../test/choose_option";
import AgentRow from "./agent_row";
import { expectPhoneDropdowns } from "../../test/phone_dropdown_checks";

const ROLES = [
  makeAgentRole({ id: "r1", name: "reviewer" }),
  makeAgentRole({ id: "r2", name: "builder" }),
];

const meta = {
  title: "Workers/AgentRow",
  component: AgentRow,
  parameters: { layout: "padded" },
  args: {
    agent: makeAgent({ name: "alpha", roles: [ROLES[0]] }),
    tenant: { allRoles: ROLES, harness: null, variants: null },
    handlers: {
      onLifecycleChanged: fn(),
      onSignedOut: fn(),
      onRolesChanged: fn(),
      onHarnessConfigSaved: fn(),
      onVariantChanged: fn(),
      onMessageSent: fn(),
    },
    grants: [],
    grantsHandle: {
      state: { status: "loaded", grants: [] },
      refresh: fn(),
      revoke: fn(),
      revokingId: null,
      revokeError: null,
    },
    messages: [],
  },
  // The row is an <li>.
  decorators: [
    (Story) => (
      <ul style={{ listStyle: "none", padding: 0 }}>
        <Story />
      </ul>
    ),
  ],
} satisfies Meta<typeof AgentRow>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Enabled: Story = {
  play: async ({ canvas }) => {
    await expect(canvas.getByText("Enabled")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Disable" })).toBeVisible();
  },
};

export const Disabled: Story = {
  args: { agent: makeAgent({ name: "alpha", disabled_at: "2026-01-02T00:00:00Z" }) },
  play: async ({ canvas }) => {
    await expect(canvas.getByText("Disabled")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Enable" })).toBeVisible();
  },
};

export const EditRolesOpen: Story = {
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Edit roles" }));
    await expect(canvas.getByRole("checkbox", { name: "reviewer" })).toBeChecked();
    await expect(canvas.getByRole("checkbox", { name: "builder" })).not.toBeChecked();
  },
};

const FAST = makeVariantVersion();
const SLOW = makeVariantVersion({ id: "aaaaaaaa-0000-0000-0000-000000000002", name: "careful", harness: "pi" });
const AGENT_ID = "11111111-1111-1111-1111-111111111111";

export const OwnerPicksAVariant: Story = {
  args: { tenant: { allRoles: ROLES, harness: null, variants: [FAST, SLOW] } },
  play: async ({ canvas, canvasElement }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Variant" }));
    const group = canvas.getByRole("group", { name: "Variant for alpha" });
    await expect(group).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Save" })).toBeDisabled();
    await chooseOption(canvasElement, "Variant", "fast v1 (claude)");
    await expect(canvas.getByRole("button", { name: "Save" })).toBeEnabled();
  },
};

export const MemberSeesNoPicker: Story = {
  args: {
    tenant: { allRoles: ROLES, harness: null, variants: null },
    agent: makeAgent({
      name: "alpha",
      variant: { name: "fast", version: 1, harness: "claude", version_id: FAST.id },
    }),
  },
  play: async ({ canvas }) => {
    await expect(canvas.getByText("variant fast v1")).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "Variant" })).toBeNull();
  },
};

export const InVariantWithInactiveOverride: Story = {
  args: {
    agent: makeAgent({
      name: "alpha",
      variant: { name: "fast", version: 1, harness: "claude", version_id: FAST.id },
      harness_override_inactive: true,
    }),
    tenant: {
      allRoles: ROLES,
      variants: [FAST],
      harness: {
        definitions: makeHarnessConfig().definitions,
        defaults: undefined,
        override: makeHarnessConfigScope({ agent_id: AGENT_ID, settings: { claude: { effort: "max" } } }),
      },
    },
  },
  play: async ({ canvas }) => {
    await expect(canvas.getByText("config override (inactive)")).toBeVisible();
    const note = canvas.getByRole("note");
    await expect(note).toHaveTextContent("applies again when the agent leaves the variant");
    await expect(note).toHaveTextContent("Effort max");
  },
};

export const PhoneVariantPicker: Story = {
  globals: { viewport: { value: "mobile1", isRotated: false } },
  args: { tenant: { allRoles: ROLES, harness: null, variants: [FAST, SLOW] } },
  play: async ({ canvas, canvasElement }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Variant" }));
    await expectPhoneDropdowns(canvasElement, 1);
  },
};

// A lease that ran out long ago is what a hold the server has not yet
// reclaimed looks like.
export const HoldingAnExpiredLease: Story = {
  args: { agent: makeAgent({ name: "alpha", lease: makeLease({ ticket_id: 7, expires_at: "2020-01-01T00:10:00Z" }) }) },
  play: async ({ canvas }) => {
    await expect(canvas.getByText("(expired)")).toBeVisible();
  },
};

export const RunOnAStaleConfig: Story = {
  args: {
    agent: makeAgent({
      name: "alpha",
      config_revision: 3,
      session: makeAgentSession({ config_revision: 2, config_status: "stale" }),
    }),
  },
  play: async ({ canvas }) => {
    await expect(canvas.getByText("stale config")).toBeVisible();
  },
};

export const MessageHintLinksHeldTicket: Story = {
  args: { agent: makeAgent({ name: "alpha", lease: makeLease({ ticket_id: 7 }) }) },
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Message" }));
    const link = await canvas.findByRole("link", { name: "comment on it" });
    await expect(link.getAttribute("href")).toContain("/tickets/7");
  },
};

export const MessageHintForIdleAgent: Story = {
  play: async ({ canvas }) => {
    await userEvent.click(canvas.getByRole("button", { name: "Message" }));
    await expect(await canvas.findByText(/comment on it/)).toBeVisible();
    await expect(canvas.queryByRole("link", { name: "comment on it" })).not.toBeInTheDocument();
  },
};
