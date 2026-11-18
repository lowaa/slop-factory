import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import { ApiError, type FleetInstructionsVersion } from "@/api";
import { makeHarnessConfig, makeVariantVersion } from "@/test/fixtures";
import { stubApi } from "@/test/story_harness";
import VariantsWidget from "./variants";

const meta = {
  title: "Variants/VariantsWidget",
  component: VariantsWidget,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 640 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof VariantsWidget>;

export default meta;
type Story = StoryObj<typeof meta>;

const FLEET_ID = "bbbbbbbb-0000-0000-0000-000000000001";
const FLEET: FleetInstructionsVersion = {
  id: FLEET_ID,
  name: "house",
  version: 3,
  kind: "fleet",
  body: "Be brief.",
  created_by: null,
  created_at: "2026-10-04T12:00:00Z",
  archived: false,
  archived_at: null,
};
const V1 = makeVariantVersion({ id: "v1", version: 1, settings: { model: "haiku" } });
const V2 = makeVariantVersion({
  id: "v2",
  version: 2,
  settings: { model: "sonnet", effort: "high" },
  fleet_instructions_version_id: FLEET_ID,
});
const PI = makeVariantVersion({ id: "v3", name: "pi-one", harness: "pi", settings: { provider: "anthropic" } });
const SAVE = "Save as a new version";

function stubVariants(listVariants: () => Promise<ReturnType<typeof makeVariantVersion>[]>, saveError?: ApiError) {
  return () =>
    stubApi({
      listVariants,
      getHarnessConfig: async () => makeHarnessConfig(),
      getFleetInstructions: async () => ({ versions: [FLEET], default_id: FLEET_ID, can_edit: true, max_body_bytes: 100 }),
      saveVariantVersion: async () => {
        if (saveError) throw saveError;
        return V2;
      },
      archiveVariantVersion: async () => V1,
      unarchiveVariantVersion: async () => V1,
      archiveVariantGroup: async () => 2,
      unarchiveVariantGroup: async () => 2,
    });
}

export const NoVariants: Story = {
  beforeEach: stubVariants(async () => []),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("No variants yet.")).toBeVisible();
    await expect(canvas.getByRole("button", { name: SAVE })).toBeDisabled();
  },
};

export const VariantsListed: Story = {
  beforeEach: stubVariants(async () => [V2, V1, PI]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("fast v2")).toBeVisible();
    await expect(canvas.getAllByRole("listitem")[0]).toHaveTextContent("house v3");
    await expect(canvas.getByText("Main model haiku")).toBeVisible();
    await expect(canvas.getByText("Provider anthropic")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Archive every fast version" })).toBeEnabled();
  },
};

export const SaveForm: Story = {
  beforeEach: stubVariants(async () => [V2]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(await canvas.findByLabelText("Name"), "fast");
    await userEvent.click(canvas.getByRole("combobox", { name: "Harness" }));
    await userEvent.click(await within(document.body).findByRole("option", { name: "pi" }));
    await expect(canvas.getByRole("group", { name: "pi" })).toBeVisible();
    await expect(canvas.queryByRole("group", { name: "Claude Code" })).toBeNull();
    await expect(canvas.getByRole("button", { name: SAVE })).toBeEnabled();
  },
};

export const SaveRefused: Story = {
  beforeEach: stubVariants(async () => [], new ApiError(422, "unknown setting")),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(await canvas.findByLabelText("Name"), "fast");
    await userEvent.click(canvas.getByRole("button", { name: SAVE }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent("unknown setting");
  },
};

export const NotAnOwner: Story = {
  beforeEach: stubVariants(async () => {
    throw new ApiError(403, "owners only");
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText(/only the tenant.s owners/i)).toBeVisible();
    await expect(canvas.queryByRole("button", { name: SAVE })).toBeNull();
  },
};

export const LoadFails: Story = {
  beforeEach: stubVariants(async () => {
    throw new ApiError(500, "Could not load variants.");
  }),
  play: async ({ canvasElement }) => {
    await expect(await within(canvasElement).findByRole("alert")).toHaveTextContent("Could not load variants.");
  },
};

export const ArchivedVersions: Story = {
  beforeEach: stubVariants(async () => [
    { ...V2, archived: true },
    { ...V1, archived: true },
    PI,
  ]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect((await canvas.findAllByText("archived")).length).toBeGreaterThan(0);
    await expect(canvas.getByRole("button", { name: "Unarchive every fast version" })).toBeEnabled();
  },
};
