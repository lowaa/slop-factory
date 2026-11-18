import { describe, expect, it, vi } from "vitest";
import { makeAttemptCard, makeTicket } from "@/test/fixtures";
import { BoardTicketFeed } from "./board_ticket_feed";

describe("BoardTicketFeed", () => {
  it("hands each changed ticket only its own cards", () => {
    const feed = new BoardTicketFeed();
    const onTicket = vi.fn();
    feed.subscribe({ onTicket, onReload: vi.fn() });
    const [a, b] = [makeTicket({ id: 1 }), makeTicket({ id: 2 })];
    const [a1, b1, b2] = [makeAttemptCard({ id: 1 }), makeAttemptCard({ id: 2 }), makeAttemptCard({ id: 2 })];

    feed.emit([a, b], [a1, b1, b2]);

    expect(onTicket).toHaveBeenCalledWith(a, [a1]);
    expect(onTicket).toHaveBeenCalledWith(b, [b1, b2]);
  });
});
