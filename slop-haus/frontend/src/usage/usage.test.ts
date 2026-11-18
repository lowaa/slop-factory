import { describe, expect, it } from "vitest";
import { BATCH_SIZE, QUEUE_CAP, Usage, type UsageEvent } from "./usage";

function setup(online = true) {
  const batches: UsageEvent[][] = [];
  const state = { online };
  const usage = new Usage((events) => batches.push(events), () => state.online);
  return { usage, batches, state };
}

describe("Usage", () => {
  it("adds the screen the person is on to a call site's event, and sends nothing until flushed", () => {
    const { usage, batches } = setup();
    usage.setScreen("approvals");
    usage.event("proposal_decision", { action: "approve" });
    expect(batches).toEqual([]);
    usage.flush();
    expect(batches).toEqual([[{ name: "proposal_decision", attrs: { action: "approve", screen: "approvals" } }]]);
  });

  it("drops an event from off any screen", () => {
    const { usage, batches } = setup();
    usage.event("proposal_decision", { action: "approve" });
    usage.flush();
    expect(batches).toEqual([]);
  });

  it("drops what is queued when offline, rather than keeping it for later", () => {
    const { usage, batches, state } = setup(false);
    usage.setScreen("board");
    usage.event("new_ticket", { action: "open_form" });
    usage.flush();
    state.online = true;
    usage.flush();
    expect(batches).toEqual([]);
  });

  it("sends a flush in requests of at most the server's batch size", () => {
    const { usage, batches } = setup();
    usage.setScreen("board");
    for (let i = 0; i < BATCH_SIZE + 1; i++) usage.event("new_ticket", { action: "open_form" });
    expect(batches).toEqual([]);
    usage.flush();
    expect(batches.map((b) => b.length)).toEqual([BATCH_SIZE, 1]);
  });

  it("holds no more than the cap between flushes, dropping the rest", () => {
    const { usage, batches } = setup();
    usage.setScreen("board");
    for (let i = 0; i < QUEUE_CAP + 25; i++) usage.event("new_ticket", { action: "open_form" });
    usage.flush();
    expect(batches.flat()).toHaveLength(QUEUE_CAP);
    usage.event("new_ticket", { action: "open_form" });
    usage.flush();
    expect(batches.flat()).toHaveLength(QUEUE_CAP + 1);
  });

  it("ignores an event the catalog does not name", () => {
    const { usage, batches } = setup();
    usage.setScreen("board");
    // @ts-expect-error not in the catalog
    usage.event("made_up", {});
    usage.flush();
    expect(batches).toEqual([]);
  });

  it("never throws from flush, which runs as the page closes", () => {
    const usage = new Usage(() => {
      throw new Error("network down");
    }, () => true);
    usage.setScreen("board");
    usage.event("new_ticket", { action: "open_form" });
    expect(() => usage.flush()).not.toThrow();
  });
});
