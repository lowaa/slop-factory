import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, BulkRefusedError } from "@/api";
import { BulkActionBar } from "@/components/bulk_action_bar";
import { makeTicket } from "@/test/fixtures";
import { chooseFromDropdown } from "@/test/widgets";
import { RecordingUsage } from "./recording_usage";
import { UsageContext } from "./use_usage";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const PREPARING = { status: "preparing" as const, allowed_moves: ["preparing", "parked", "done", "cancelled"] as const, can_ready: true };

function renderBar(usage: RecordingUsage, count: number) {
  const selected = Array.from({ length: count }, (_, i) => makeTicket({ id: i + 1, ...PREPARING, allowed_moves: [...PREPARING.allowed_moves] }));
  render(
    <UsageContext.Provider value={usage}>
      <BulkActionBar selected={selected} onApplied={vi.fn()} onRefused={vi.fn()} onDeselect={vi.fn()} onExit={vi.fn()} />
    </UsageContext.Provider>,
  );
}

function board(): RecordingUsage {
  const usage = new RecordingUsage();
  usage.setScreen("board");
  return usage;
}

// The call sites are where an event's name and attributes are chosen, so
// they are held to what each outcome should send. The counts are buckets: an
// exact number of tickets is never sent.
describe("bulk action usage events", () => {
  it("counts a move that went through, with the size of the selection as a bucket", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "bulkSetTicketStatus").mockResolvedValue([]);
    const usage = board();
    renderBar(usage, 3);

    await chooseFromDropdown(user, "Move to", "Parked");

    expect(usage.named("bulk_action")).toEqual([{ screen: "board", action: "move", outcome: "done", count_bucket: "2-5" }]);
  });

  it("counts a ready of one ticket as mark_ready", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "bulkMarkTicketsReady").mockResolvedValue([]);
    const usage = board();
    renderBar(usage, 1);

    await chooseFromDropdown(user, "Move to", "Open");

    expect(usage.named("bulk_action")).toEqual([{ screen: "board", action: "mark_ready", outcome: "done", count_bucket: "1" }]);
  });

  it("counts a refusal and an error apart from a success", async () => {
    const user = userEvent.setup();
    const bulk = vi.spyOn(apiClient, "bulkSetTicketPriority");
    bulk.mockRejectedValueOnce(new BulkRefusedError(409, "refused", [{ id: 1, status: "open", message: "no" }]));
    bulk.mockRejectedValueOnce(new ApiError(500, "boom"));
    const usage = board();
    renderBar(usage, 2);

    await chooseFromDropdown(user, "Priority", "urgent");
    await chooseFromDropdown(user, "Priority", "low");

    expect(usage.named("bulk_action").map((e) => [e.action, e.outcome])).toEqual([
      ["priority", "refused"],
      ["priority", "error"],
    ]);
  });

  it("sends nothing from a bar rendered outside a provider", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "bulkSetTicketPriority").mockResolvedValue([]);
    render(<BulkActionBar selected={[makeTicket({ id: 1 })]} onApplied={vi.fn()} onRefused={vi.fn()} onDeselect={vi.fn()} onExit={vi.fn()} />);
    await chooseFromDropdown(user, "Priority", "low");
    expect(screen.getByRole("status")).toBeInTheDocument();
  });
});
