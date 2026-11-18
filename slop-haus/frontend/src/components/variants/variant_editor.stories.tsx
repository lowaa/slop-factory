import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import type { FleetInstructionsVersion } from "@/api";
import { makeHarnessConfig } from "@/test/fixtures";
import VariantEditor from "./variant_editor";
import { EMPTY_DRAFT } from "./variant_draft";

const FLEET: FleetInstructionsVersion[] = [
  {
    id: "bbbbbbbb-0000-0000-0000-000000000001",
    name: "house",
    version: 3,
    kind: "fleet",
    body: "Be brief.",
    created_by: null,
    created_at: "2026-10-04T12:00:00Z",
    archived: false,
    archived_at: null,
  },
];

const meta = {
  title: "Variants/VariantEditor",
  component: VariantEditor,
  args: {
    start: EMPTY_DRAFT,
    definitions: makeHarnessConfig().definitions,
    fleetVersions: FLEET,
    onSave: fn(async () => {}),
  },
  beforeEach: ({ args }) => {
    (args.onSave as ReturnType<typeof fn>).mockClear();
  },
} satisfies Meta<typeof VariantEditor>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Empty: Story = {
  play: async ({ canvas }) => {
    await expect(canvas.getByRole("button", { name: "Save as a new version" })).toBeDisabled();
    // "None" is the default choice; the tenant's default is not offered.
    await expect(canvas.getByRole("combobox", { name: "Fleet instructions" })).toHaveTextContent("None");
    await expect(canvas.queryByText(/tenant default/i)).toBeNull();
  },
};

export const SavesWithTheChosenFleetInstructions: Story = {
  play: async ({ canvas, args }) => {
    await userEvent.type(canvas.getByLabelText("Name"), "fast");
    await userEvent.click(canvas.getByRole("combobox", { name: "Fleet instructions" }));
    await userEvent.click(await within(document.body).findByRole("option", { name: /^house/ }));
    await userEvent.click(canvas.getByRole("button", { name: "Save as a new version" }));
    await expect(args.onSave).toHaveBeenCalledWith({
      name: "fast",
      harness: "claude",
      settings: {},
      fleetVersionId: FLEET[0].id,
    });
  },
};
