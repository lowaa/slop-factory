import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, within } from "storybook/test";
import ReplayBatchPanel from "./replay_batch_panel";
import { DEFAULT_VARIANT, planBatch, type ReplayBatchActions } from "./use_replay_batch";

const limits = { max_attempts: 20, max_per_ticket: 5 };
const challengers = [{ id: "v1", name: "Careful", version: 2, harness: "claude" as const }];

function batchState(overrides: Partial<ReplayBatchActions> = {}): ReplayBatchActions {
  const variantIds = overrides.variantIds ?? [DEFAULT_VARIANT];
  const runs = overrides.runs ?? 1;
  return {
    limits,
    unavailable: false,
    variantIds,
    runs,
    plan: planBatch(3, variantIds.length, runs, limits),
    confirming: false,
    result: null,
    submit: { busy: false, error: null, run: fn() } as unknown as ReplayBatchActions["submit"],
    toggleVariant: fn(),
    setRuns: fn(),
    review: fn(),
    cancelConfirm: fn(),
    confirm: fn(),
    ...overrides,
  };
}

const meta = {
  title: "Replay batch/Panel",
  component: ReplayBatchPanel,
  args: { ticketCount: 3, variants: challengers, batch: batchState() },
} satisfies Meta<typeof ReplayBatchPanel>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("status")).toHaveTextContent("This adds 3 replays");
    await expect(canvas.getByLabelText(/Default: agents in no variant/)).toBeChecked();
  },
};

export const OverTheMaximum: Story = {
  args: { batch: batchState({ variantIds: [DEFAULT_VARIANT, "v1"], runs: 4 }) },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("alert")).toHaveTextContent(/over the maximum of 20|at most 5 live attempts/);
    await expect(canvas.getByRole("button", { name: /^Review/ })).toBeDisabled();
  },
};

export const Confirming: Story = {
  args: { batch: batchState({ confirming: true }) },
};

export const PartlyRefused: Story = {
  args: {
    batch: batchState({
      result: {
        batch_id: "b1",
        added: [{ ticket_id: 1, attempts: [2] }],
        refused: [{ ticket_id: 2, reason: "ticket #2 already has 5 live attempts" }],
      },
    }),
  },
};
