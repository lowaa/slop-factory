import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { apiClient, type QueueBlockage } from "@/api";
import { renderSignedIn } from "@/test/render_signed_in";
import QueueBlockagePanel from "./queue_blockage_panel";

function makeBlockage(overrides: Partial<QueueBlockage> = {}): QueueBlockage {
  return {
    ticket_id: 9,
    ticket_title: "Waiting work",
    holder_ticket_id: 3,
    holder_title: "Held work",
    holder_status: "in_progress",
    holder_agent_name: "builder-1",
    path: "server/",
    ...overrides,
  };
}

describe("QueueBlockagePanel", () => {
  it("names the waiting ticket, the scope and who holds it", async () => {
    vi.spyOn(apiClient, "getQueueBlockage").mockResolvedValue(makeBlockage());

    await renderSignedIn(<QueueBlockagePanel refreshToken={0} />);

    const panel = await screen.findByRole("status", { name: "Queue blocked" });
    expect(panel).toHaveTextContent("#9 Waiting work");
    expect(panel).toHaveTextContent("server/");
    expect(panel).toHaveTextContent("#3 Held work (in progress by builder-1)");
    expect(panel).toHaveTextContent("frees when that ticket finishes");
  });

  it("says a person is needed when the scope is held by a ticket in review", async () => {
    vi.spyOn(apiClient, "getQueueBlockage").mockResolvedValue(
      makeBlockage({ holder_status: "review", holder_agent_name: "" }),
    );

    await renderSignedIn(<QueueBlockagePanel refreshToken={0} />);

    expect(await screen.findByRole("status", { name: "Queue blocked" })).toHaveTextContent(
      "waiting on a person to approve",
    );
  });

  it("shows nothing when the queue is not blocked", async () => {
    const spy = vi.spyOn(apiClient, "getQueueBlockage").mockResolvedValue(null);

    await renderSignedIn(<QueueBlockagePanel refreshToken={0} />);

    await vi.waitFor(() => expect(spy).toHaveBeenCalled());
    expect(screen.queryByRole("status", { name: "Queue blocked" })).toBeNull();
  });

  it("is fetched again when the board's live refresh fires, and clears", async () => {
    const spy = vi.spyOn(apiClient, "getQueueBlockage").mockResolvedValue(makeBlockage());
    const { rerender } = await renderSignedIn(<QueueBlockagePanel refreshToken={0} />);
    await screen.findByRole("status", { name: "Queue blocked" });

    spy.mockResolvedValue(null);
    rerender(<QueueBlockagePanel refreshToken={1} />);

    await vi.waitFor(() => expect(screen.queryByRole("status", { name: "Queue blocked" })).toBeNull());
  });
});
