import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import { makeAgent, makeHarnessConfig, makeSetting, makeTenant } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import StewardSection from "./steward_section";
import "./tenant_settings_page.scss";

const meta = {
  title: "Settings/StewardSection",
  component: StewardSection,
  decorators: [
    (Story) => (
      <div className="tenant-settings">
        <Story />
      </div>
    ),
  ],
  beforeEach: () =>
    stubApi({
      getStewardDigest: async () => ({ categories: [], decisions: [], decisions_cut: false, total: { assessed: 0, needs_human: 0, awaiting: 0, unclassified: 0, agreed: 0, disagreed: 0 } }) as never,
      getWorkspace: async () => ({ manifest: null }) as never,
      getHarnessConfig: async () => makeHarnessConfig(),
      listAgents: async () => [makeAgent({ kind: "steward" })],
      getFleetInstructions: async () =>
        ({ default_id: "w1", versions: [{ id: "w1", body: "salary" }] }) as never,
      listTenants: async () => [makeTenant({ is_owner: true })],
      listSettings: async () => [
        makeSetting({
          name: "steward_level",
          type: "int",
          int_val: 3,
          bool_val: null,
          is_default: false,
        }),
        makeSetting({
          name: "steward_veto_minutes",
          type: "int",
          int_val: 60,
          bool_val: null,
        }),
        makeSetting({
          name: "steward_daily_cap",
          type: "int",
          int_val: 50,
          bool_val: null,
        }),
        makeSetting({
          name: "steward_category_tiers",
          type: "string",
          string_val: '{"priority":"human"}',
          bool_val: null,
        }),
      ],
    }),
} satisfies Meta<typeof StewardSection>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Owner: Story = {
  play: async ({ canvasElement }) => {
    const section = within(await within(canvasElement).findByRole("region", { name: "Steward" }));
    await expect(section.getByRole("radio", { name: /3 Routine/ })).toBeChecked();
    await expect(section.getByRole("combobox", { name: "Change a ticket's priority" })).toHaveTextContent("human");
  },
};
