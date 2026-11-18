import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, waitFor, within } from "storybook/test";
import { ApiError, type FleetInstructions } from "@/api";
import { stubApi } from "@/test/story_harness";
import FleetInstructionsWidget from "./fleet_instructions";

const meta = {
  title: "Fleet instructions/FleetInstructionsWidget",
  component: FleetInstructionsWidget,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 640 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof FleetInstructionsWidget>;

export default meta;
type Story = StoryObj<typeof meta>;

const V1 = {
  id: "id-1",
  name: "house",
  version: 1,
  kind: "fleet" as const,
  body: "Be brief.",
  created_by: null,
  created_at: "2026-10-04T12:00:00Z",
  archived: false,
  archived_at: null,
};
const V2 = { ...V1, id: "id-2", version: 2, body: "Comment a plan first." };
const SAVE = "Save as a new version";

const saved: { name: string; body: string; makeDefault: boolean }[] = [];

const OLD = {
  ...V1,
  id: "id-old",
  name: "legacy",
  body: "Old text.",
  archived: true,
  archived_at: "2026-10-05T12:00:00Z",
};

function stubInstructions(
  overrides: Partial<FleetInstructions>,
  saveError?: ApiError,
  archiveError?: ApiError,
) {
  saved.length = 0;
  const data: FleetInstructions = {
    versions: [],
    default_id: null,
    can_edit: true,
    max_body_bytes: 100,
    ...overrides,
  };
  stubApi({
    getFleetInstructions: async (includeArchived = false) => ({
      ...data,
      versions: data.versions.filter((v) => includeArchived || !v.archived),
    }),
    archiveFleetInstructionsVersion: async () => {
      if (archiveError) throw archiveError;
      return V1;
    },
    getFleetBriefings: async () => [],
    saveFleetInstructionsVersion: async (name, body, makeDefault) => {
      saved.push({ name, body, makeDefault });
      if (saveError) throw saveError;
      return V1;
    },
  });
}

export const NoInstructions: Story = {
  beforeEach: () => stubInstructions({}),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(
      await canvas.findByText(/no default is chosen/i),
    ).toBeVisible();
    await expect(canvas.getByLabelText("Name")).toBeEnabled();
    await expect(canvas.getByLabelText("Instructions")).toBeEnabled();
    await expect(
      canvas.getByRole("checkbox", { name: /make this version the default/i }),
    ).not.toBeChecked();
    await expect(canvas.getByRole("button", { name: SAVE })).toBeDisabled();
  },
};

export const InstructionsListed: Story = {
  beforeEach: () =>
    stubInstructions({ versions: [V2, V1], default_id: "id-1" }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    // The default is open; the older version starts folded.
    await expect(await canvas.findByText("Be brief.")).toBeVisible();
    await expect(canvas.queryByText("Comment a plan first.")).toBeNull();
    await expect(canvas.getByText("default")).toBeVisible();
    await expect(
      canvas.getByRole("button", { name: "Use no fleet instructions" }),
    ).toBeEnabled();
    await expect(
      canvas.queryByRole("button", { name: "Make house v1 the default" }),
    ).toBeNull();

    await userEvent.click(
      canvas.getByRole("button", { name: "Expand house v2" }),
    );
    await expect(canvas.getByText("Comment a plan first.")).toBeVisible();
    await expect(
      canvas.getByRole("button", { name: "Make house v2 the default" }),
    ).toBeEnabled();
  },
};

export const HistoryFolded: Story = {
  beforeEach: () =>
    stubInstructions({ versions: [V2, V1], default_id: "id-1" }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /hide version history/i }),
    );
    await expect(canvas.queryByText("Be brief.")).toBeNull();
    await expect(
      canvas.getByRole("button", { name: /show version history/i }),
    ).toHaveAttribute("aria-expanded", "false");
  },
};

export const ReadOnly: Story = {
  beforeEach: () =>
    stubInstructions({ versions: [V1], default_id: "id-1", can_edit: false }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("Be brief.")).toBeVisible();
    await expect(canvas.queryByLabelText("Name")).toBeNull();
    await expect(canvas.queryByLabelText("Instructions")).toBeNull();
    await expect(canvas.queryByRole("button", { name: SAVE })).toBeNull();
    await expect(
      canvas.queryByRole("button", { name: /new version from/i }),
    ).toBeNull();
    await expect(
      canvas.queryByRole("button", { name: /make .* the default/i }),
    ).toBeNull();
    await expect(
      canvas.getByText(/only the tenant.s owners can change/i),
    ).toBeVisible();
  },
};

export const SaveVersion: Story = {
  beforeEach: () => stubInstructions({}),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(await canvas.findByLabelText("Name"), "house");
    await userEvent.type(canvas.getByLabelText("Instructions"), "Be brief.");
    await userEvent.click(canvas.getByRole("checkbox", { name: /make this version the default/i }));
    await userEvent.click(canvas.getByRole("button", { name: SAVE }));

    await expect(saved).toEqual([
      { name: "house", body: "Be brief.", makeDefault: true },
    ]);
    // The body and the default choice reset; the name stays for the next version.
    await waitFor(() =>
      expect(canvas.getByLabelText("Instructions")).toHaveValue(""),
    );
    await expect(canvas.getByLabelText("Name")).toHaveValue("house");
    await expect(canvas.getByRole("checkbox", { name: /make this version the default/i })).not.toBeChecked();
    await expect(canvas.queryByRole("alert")).toBeNull();
  },
};

export const SaveError: Story = {
  beforeEach: () =>
    stubInstructions(
      { versions: [V1], default_id: "id-1" },
      new ApiError(500, "database unavailable"),
    ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", {
        name: "Start a new version from house v1",
      }),
    );
    await expect(canvas.getByLabelText("Name")).toHaveValue("house");
    await userEvent.type(canvas.getByLabelText("Instructions"), " Again.");
    await userEvent.click(canvas.getByRole("checkbox", { name: /make this version the default/i }));
    await userEvent.click(canvas.getByRole("button", { name: SAVE }));

    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "database unavailable",
    );
    // The draft is kept so the save can be retried.
    await expect(canvas.getByLabelText("Instructions")).toHaveValue(
      "Be brief. Again.",
    );
    await expect(canvas.getByRole("checkbox", { name: /make this version the default/i })).toBeChecked();
    await expect(canvas.getByRole("button", { name: SAVE })).toBeEnabled();
  },
};

export const ArchivedHiddenByDefault: Story = {
  beforeEach: () =>
    stubInstructions({ versions: [V1, OLD], default_id: "id-1" }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("Be brief.")).toBeVisible();
    await expect(
      canvas.getByRole("checkbox", { name: "Show archived" }),
    ).not.toBeChecked();
    await expect(canvas.queryByText("legacy")).toBeNull();
    await expect(canvas.queryByText("archived")).toBeNull();
  },
};

export const ShowArchived: Story = {
  beforeEach: () =>
    stubInstructions({ versions: [V1, OLD], default_id: "id-1" }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("checkbox", { name: "Show archived" }),
    );
    await expect(await canvas.findByText("legacy")).toBeVisible();
    await expect(canvas.getAllByText("archived")).not.toHaveLength(0);
    await expect(
      canvas.getByRole("button", { name: "Unarchive legacy v1" }),
    ).toBeEnabled();
    await expect(
      canvas.getByRole("button", { name: "Unarchive every legacy version" }),
    ).toBeEnabled();
  },
};

export const DefaultCannotBeArchived: Story = {
  beforeEach: () =>
    stubInstructions({ versions: [V2, V1], default_id: "id-1" }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const button = await canvas.findByRole("button", {
      name: "Archive house v1",
    });
    await expect(button).toHaveAttribute("aria-disabled", "true");
    button.focus();
    await expect(await canvas.findByRole("tooltip")).toHaveTextContent(
      /default can't be archived/,
    );
    await expect(button).toHaveAccessibleDescription(
      /default can't be archived/,
    );
  },
};

export const ArchiveRefused: Story = {
  beforeEach: () =>
    stubInstructions(
      { versions: [V2, V1], default_id: "id-9" },
      undefined,
      new ApiError(409, "the default version can't be archived"),
    ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Archive house v2" }),
    );
    await expect(await canvas.findByRole("alert")).toHaveTextContent(
      "the default version can't be archived",
    );
  },
};

const WORDS = { ...V1, id: "w1", name: "words", kind: "sensitive_words" as const, body: "secret\npassword" };

function stubKinds(words: Partial<FleetInstructions>, level: number) {
  const base = { versions: [], default_id: null, can_edit: true, max_body_bytes: 100 };
  stubApi({
    getFleetInstructions: async (_includeArchived = false, kind = "fleet") => ({
      ...base,
      ...(kind === "sensitive_words" ? words : {}),
    }),
    getFleetBriefings: async () => [],
    listSettings: async () => [
      { name: "steward_level", type: "int", int_val: level, bool_val: null, string_val: null, is_default: false, updated_at: "2026-10-01T00:00:00Z" } as never,
    ],
  });
}

export const StewardTab: Story = {
  beforeEach: () => stubKinds({}, 3),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Steward" }));
    await expect(await canvas.findByText(/can.t loosen the server.s checks/)).toBeInTheDocument();
  },
};

export const SensitiveWordsEmptyWarning: Story = {
  beforeEach: () => stubKinds({}, 4),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Sensitive words" }));
    await expect(await canvas.findByRole("alert")).toHaveTextContent(/no sensitive words are pinned/i);
  },
};

export const SensitiveWordsPinned: Story = {
  beforeEach: () => stubKinds({ versions: [WORDS], default_id: "w1" }, 5),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Sensitive words" }));
    await expect(await canvas.findByText(/one word or phrase per line/i)).toBeInTheDocument();
    await expect(canvas.queryByRole("alert")).toBeNull();
  },
};
