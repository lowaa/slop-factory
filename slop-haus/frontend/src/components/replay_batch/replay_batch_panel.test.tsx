import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "@/api";
import ReplayBatchPanel from "./replay_batch_panel";
import { planBatch, useReplayBatch, type ReplayBatchRunner } from "./use_replay_batch";

vi.mock("@/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api")>()),
  apiClient: { replayBatchLimits: vi.fn(), addReplayBatch: vi.fn() },
}));

const limits = { max_attempts: 6, max_per_ticket: 5 };
const challenger = { id: "v1", name: "Careful", version: 2 } as never;

function Harness({ ids }: { ids: number[] }) {
  const batch = useReplayBatch(ids);
  return <ReplayBatchPanel ticketCount={ids.length} variants={[challenger]} batch={batch} />;
}

function RunnerHarness({ ids, run }: { ids: number[]; run: ReplayBatchRunner }) {
  const batch = useReplayBatch(ids, run);
  return <ReplayBatchPanel ticketCount={ids.length} variants={[challenger]} batch={batch} />;
}

beforeEach(() => {
  vi.mocked(apiClient.replayBatchLimits).mockResolvedValue(limits);
  vi.mocked(apiClient.addReplayBatch).mockReset();
});

describe("planBatch", () => {
  it("counts tickets x variants x runs and says why a batch cannot start", () => {
    expect(planBatch(2, 2, 2, limits)).toEqual({ count: 8, perTicket: 4, blocked: expect.stringMatching(/over the maximum of 6/) });
    expect(planBatch(1, 3, 2, limits).blocked).toMatch(/at most 5 live attempts/);
    expect(planBatch(2, 1, 3, limits)).toEqual({ count: 6, perTicket: 3, blocked: null });
    expect(planBatch(0, 1, 1, limits).blocked).toMatch(/at least one ticket/);
  });
});

describe("ReplayBatchPanel", () => {
  it("shows the count and the cap, offers the default beside challengers, and warns once", async () => {
    render(<Harness ids={[1, 2]} />);

    expect(await screen.findByText(/at most 6/)).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "" })).toHaveTextContent("This adds 2 replays (2 tickets x 1 variant x 1 run)");
    expect(screen.getByLabelText(/Default: agents in no variant/)).toBeChecked();
    expect(screen.getByLabelText(/Careful v2/)).not.toBeChecked();
    expect(screen.getAllByRole("note")).toHaveLength(1);
  });

  it("sends nothing until the count is confirmed, then reports the refused tickets", async () => {
    vi.mocked(apiClient.addReplayBatch).mockResolvedValue({
      batch_id: "b",
      added: [{ ticket_id: 1, attempts: [2, 3] }],
      refused: [{ ticket_id: 2, reason: "ticket #2 already has 5 live attempts" }],
    });
    const user = userEvent.setup();
    render(<Harness ids={[1, 2]} />);
    await screen.findByText(/at most 6/);

    await user.click(screen.getByLabelText(/Careful v2/));
    await user.click(screen.getByRole("button", { name: "Review 4 replays" }));
    expect(apiClient.addReplayBatch).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Confirm: add 4 replays" }));

    expect(apiClient.addReplayBatch).toHaveBeenCalledWith([1, 2], [null, "v1"], 1, 4);
    expect(await screen.findByText(/Added 2 replays to 1 ticket/)).toBeInTheDocument();
    expect(screen.getByText(/#2: ticket #2 already has 5 live attempts/)).toBeInTheDocument();
  });

  it("withdraws the confirmation when the plan changes", async () => {
    const user = userEvent.setup();
    render(<Harness ids={[1]} />);
    await screen.findByText(/at most 6/);
    await user.click(screen.getByRole("button", { name: "Review 1 replay" }));
    expect(screen.getByRole("group", { name: "Confirm replays" })).toBeInTheDocument();

    await user.click(screen.getByLabelText(/Careful v2/));

    expect(screen.queryByRole("group", { name: "Confirm replays" })).not.toBeInTheDocument();
  });

  it("refuses to review a batch over the maximum", async () => {
    const user = userEvent.setup();
    render(<Harness ids={[1, 2, 3, 4, 5, 6, 7]} />);
    await screen.findByText(/over the maximum of 6/);

    expect(screen.getByRole("button", { name: "Review 7 replays" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Review 7 replays" }));
    expect(screen.queryByRole("group", { name: "Confirm replays" })).not.toBeInTheDocument();
  });

  it("sends through the given runner instead of the default endpoint, with the confirmed count", async () => {
    const run = vi.fn<ReplayBatchRunner>().mockResolvedValue({ batch_id: "b", added: [], refused: [] });
    const user = userEvent.setup();
    render(<RunnerHarness ids={[1, 2]} run={run} />);
    await screen.findByText(/at most 6/);

    await user.click(screen.getByLabelText(/Careful v2/));
    await user.click(screen.getByRole("button", { name: "Review 4 replays" }));
    await user.click(screen.getByRole("button", { name: "Confirm: add 4 replays" }));

    expect(run).toHaveBeenCalledWith([null, "v1"], 1, 4);
    expect(apiClient.addReplayBatch).not.toHaveBeenCalled();
  });
});
