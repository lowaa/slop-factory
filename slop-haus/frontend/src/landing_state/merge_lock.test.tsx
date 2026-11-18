import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { apiClient } from "@/api";
import { makeTicket } from "@/test/fixtures";
import { TicketCard } from "@/components/ticket_card";
import { renderSignedIn } from "@/test/render_signed_in";
import { describeMergeLock, MergeLockProvider, useMergeLockState } from ".";

function State({ ticketId }: { ticketId: number }) {
  const state = useMergeLockState(ticketId);
  return <span>{state ? describeMergeLock(state) : "none"}</span>;
}

const waiter = (ticket_id: number) => ({
  ticket_id,
  ticket_title: "t",
  agent_id: "a",
  agent_name: "a",
  priority: "normal" as const,
  queued_at: "2026-01-01T00:00:00Z",
  waited_seconds: 1,
});

describe("MergeLockProvider", () => {
  it("names the holder, and each waiter by its place in the queue", async () => {
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([
      {
        name: "default",
        holder: {
          ticket_id: 1,
          ticket_title: "t",
          agent_id: "a",
          agent_name: "a",
          acquired_at: "2026-01-01T00:00:00Z",
          dead: false,
          held_seconds: 1,
          last_heartbeat_at: "2026-01-01T00:00:00Z",
          long_held: false,
          since_heartbeat_seconds: 1,
        },
        queue: [waiter(2), waiter(3)],
      },
    ]);
    await renderSignedIn(
      <MergeLockProvider refreshToken={0}>
        <State ticketId={1} />
        <State ticketId={2} />
        <State ticketId={3} />
        <State ticketId={4} />
      </MergeLockProvider>,
    );
    expect(await screen.findByText("holds the merge lock")).toBeInTheDocument();
    expect(
      screen.getByText("waiting for the merge lock (1st)"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("waiting for the merge lock (2nd)"),
    ).toBeInTheDocument();
    expect(screen.getByText("none")).toBeInTheDocument();
  });
});

describe("TicketCard merge lock badge", () => {
  it.each([
    ["in_progress", true],
    ["open", false],
  ] as const)("on a %s ticket shows the lock state: %s", async (status, shown) => {
    vi.spyOn(apiClient, "listIntegrationLocks").mockResolvedValue([{ name: "default", queue: [waiter(5)] }]);
    await renderSignedIn(
      <MergeLockProvider refreshToken={0}>
        <TicketCard ticket={makeTicket({ id: 5, status })} />
      </MergeLockProvider>,
    );
    await vi.waitFor(() => expect(apiClient.listIntegrationLocks).toHaveBeenCalled());
    if (shown) expect(await screen.findByText("waiting for the merge lock (1st)")).toBeInTheDocument();
    else expect(screen.queryByText(/merge lock/)).not.toBeInTheDocument();
  });
});
