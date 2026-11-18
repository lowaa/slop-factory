import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import type { EvalSet } from "@/api";
import { stubApi } from "@/test/story_harness";
import EvalSetsPage from "./eval_sets_page";

const BASE = { description: "", created_by: null, created_at: "2026-10-04T12:00:00Z", updated_at: "2026-10-04T12:00:00Z" };
const FILTER_SET: EvalSet = { ...BASE, id: "s1", name: "Logins", filter: { statuses: ["done"], q: "login" }, ticket_ids: [] };
const PINNED: EvalSet = { ...BASE, id: "s2", name: "Pinned", filter: null, ticket_ids: [4, 5] };

const meta = {
  title: "Eval sets/Page",
  component: EvalSetsPage,
  parameters: { layout: "padded" },
  beforeEach: () =>
    stubApi({
      listEvalSets: async () => [FILTER_SET, PINNED],
      listVariantOptions: async () => [{ id: "v1", name: "Careful", version: 2, harness: "claude" as const }],
      replayBatchLimits: async () => ({ max_attempts: 20, max_per_ticket: 5 }),
      listEvalSetBatches: async () => [
        { batch_id: "b1", created_at: "2026-10-05T12:00:00Z", created_by: null, runs: 1, ticket_count: 2, version_ids: ["v1"] },
      ],
    }),
} satisfies Meta<typeof EvalSetsPage>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(await canvas.findByText("2 pinned")).toBeInTheDocument();
  },
};

export const PinnedSetWithHistory: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Pinned" }));
    await expect(await canvas.findByText(/Careful v2 — 1 run on 2 tickets/)).toBeInTheDocument();
    await userEvent.click(canvas.getByRole("button", { name: "Run variant…" }));
    await expect(await canvas.findByText(/One batch may add at most 20/)).toBeInTheDocument();
  },
};

export const FilterSet: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Logins" }));
    await expect(canvas.getByRole("button", { name: "Pin current matches" })).toBeInTheDocument();
  },
};

export const NewSet: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "New eval set" }));
    await expect(canvas.getByRole("form", { name: "New eval set" })).toBeInTheDocument();
  },
};
