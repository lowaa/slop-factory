import { describe, expect, it } from "vitest";
import { makeTicket } from "@/test/fixtures";
import { canReadyAll, commonMoves, noCommonMoveReason } from "./bulk_moves";

describe("commonMoves", () => {
  it("is the moves every ticket allows, in the server's order", () => {
    const tickets = [
      makeTicket({ status: "open", allowed_moves: ["preparing", "open", "in_progress", "done", "cancelled"] }),
      makeTicket({ status: "review", allowed_moves: ["preparing", "discussion", "review", "done", "cancelled"] }),
    ];

    expect(commonMoves(tickets)).toEqual(["preparing", "done", "cancelled"]);
  });

  it("leaves out the status every ticket is already in", () => {
    const tickets = [
      makeTicket({ status: "preparing", allowed_moves: ["preparing", "parked", "done"] }),
      makeTicket({ status: "preparing", allowed_moves: ["preparing", "parked", "done"] }),
    ];

    expect(commonMoves(tickets)).toEqual(["parked", "done"]);
  });

  it("keeps a status only some of the tickets are in", () => {
    const tickets = [
      makeTicket({ status: "open", allowed_moves: ["open", "blocked"] }),
      makeTicket({ status: "blocked", allowed_moves: ["open", "blocked"] }),
    ];

    expect(commonMoves(tickets)).toEqual(["open", "blocked"]);
  });

  it("is empty when the tickets have no move in common", () => {
    const tickets = [
      makeTicket({ status: "preparing", allowed_moves: ["preparing", "parked"] }),
      makeTicket({ status: "in_progress", allowed_moves: ["open", "in_progress"] }),
    ];

    expect(commonMoves(tickets)).toEqual([]);
  });

  it("is empty for no tickets", () => {
    expect(commonMoves([])).toEqual([]);
  });
});

describe("canReadyAll", () => {
  it("is true only when every ticket can be readied", () => {
    expect(canReadyAll([makeTicket({ can_ready: true }), makeTicket({ can_ready: true })])).toBe(true);
    expect(canReadyAll([makeTicket({ can_ready: true }), makeTicket({ can_ready: false })])).toBe(false);
    expect(canReadyAll([])).toBe(false);
  });
});

describe("noCommonMoveReason", () => {
  it("names the columns the selection spans", () => {
    const tickets = [
      makeTicket({ status: "preparing" }),
      makeTicket({ status: "in_progress" }),
      makeTicket({ status: "preparing" }),
      makeTicket({ status: "review" }),
    ];

    expect(noCommonMoveReason(tickets)).toBe(
      "No move fits them all: Preparing, In progress and Review tickets allow different moves.",
    );
  });
});
