import { describe, expect, it } from "vitest";
import type { ConversationEvent } from "@/api";
import { buildTranscript, currentActivity, isReplying, isStopping, lastSeq, mergeEvents, runningTurn, turnStartedAt } from "./transcript";

function ev(seq: number, kind: ConversationEvent["kind"], body = "", turn: number | null = null, block: string | null = null): ConversationEvent {
  return { seq, kind, body, turn, block, created_at: "2026-01-01T00:00:00Z" };
}

describe("mergeEvents", () => {
  it("keeps each seq once and in order, however the events arrive", () => {
    const have = [ev(1, "user", "hi"), ev(2, "assistant", "a", 1)];
    const merged = mergeEvents(have, [ev(3, "assistant", "b", 1), ev(2, "assistant", "a", 1), ev(1, "user", "hi")]);
    expect(merged.map((e) => e.seq)).toEqual([1, 2, 3]);
    expect(lastSeq(merged)).toBe(3);
  });

  it("returns what it had when nothing is new", () => {
    const have = [ev(1, "user", "hi")];
    expect(mergeEvents(have, [])).toBe(have);
  });
});

describe("buildTranscript", () => {
  it("makes one reply of the batches of a turn, and a new one of the next turn", () => {
    const items = buildTranscript([
      ev(1, "user", "q"),
      ev(2, "assistant", "Hel", 1),
      ev(3, "assistant", "lo", 1),
      ev(4, "turn_end", "", 1),
      ev(5, "assistant", "More", 2),
    ]);
    expect(items).toEqual([
      { type: "user", seq: 1, text: "q", at: "2026-01-01T00:00:00Z" },
      { type: "assistant", seq: 2, turn: 1, parts: [{ type: "text", block: null, text: "Hello" }], done: true },
      { type: "assistant", seq: 5, turn: 2, parts: [{ type: "text", block: null, text: "More" }], done: false },
    ]);
  });

  it("states what the host reported as notes", () => {
    const items = buildTranscript([ev(1, "ready", ""), ev(2, "failed", "claude not found")]);
    expect(items.map((i) => (i.type === "note" ? i.text : ""))).toEqual(["Connected", "Could not start: claude not found"]);
  });

  it("says why it was disconnected, or just that it was, and when the rest was not kept", () => {
    const items = buildTranscript([ev(1, "stopped", "disconnected by its owner"), ev(2, "stopped", ""), ev(3, "truncated", "")]);
    expect(items.map((i) => (i.type === "note" ? i.text : ""))).toEqual([
      "Disconnected by its owner",
      "Disconnected",
      "The rest of the reply was not kept: this conversation is at its size limit.",
    ]);
  });

  it("notes a stopped turn once it has ended, and leaves the request to the Stop button", () => {
    const items = buildTranscript([ev(1, "user", "q"), ev(2, "interrupt_requested", "", 1), ev(3, "turn_end", "", 1), ev(4, "interrupted", "", 1)]);
    expect(items.map((i) => (i.type === "note" ? i.text : i.type))).toEqual(["user", "Stopped by you."]);
  });

  it("shows no note for a stop that came too late", () => {
    const items = buildTranscript([ev(1, "user", "q"), ev(2, "assistant", "a", 1), ev(3, "turn_end", "", 1), ev(4, "interrupt_requested", "", 1)]);
    expect(items.map((i) => i.type)).toEqual(["user", "assistant"]);
  });

  it("keeps markup as the text it is", () => {
    const [item] = buildTranscript([ev(1, "assistant", "<img src=x onerror=alert(1)>", 1)]);
    expect(item).toMatchObject({ parts: [{ text: "<img src=x onerror=alert(1)>" }] });
  });
});

describe("isReplying", () => {
  it("is true while a reply is open or unanswered, false once its turn ends", () => {
    expect(isReplying(buildTranscript([ev(1, "user", "q")]))).toBe(true);
    expect(isReplying(buildTranscript([ev(1, "user", "q"), ev(2, "assistant", "a", 1)]))).toBe(true);
    expect(isReplying(buildTranscript([ev(1, "user", "q"), ev(2, "assistant", "a", 1), ev(3, "turn_end", "", 1)]))).toBe(false);
    expect(isReplying([])).toBe(false);
  });

  it("is false once a turn ends over a reply the host sent without a turn, with no repo state after", () => {
    const items = buildTranscript([ev(1, "user", "q"), ev(2, "assistant", "a"), ev(3, "turn_end", "", 1)]);
    expect(items[1]).toMatchObject({ type: "assistant", turn: null, done: true });
    expect(isReplying(items)).toBe(false);
  });

  it("ends a turn's reply only, not one the owner has not yet seen answered", () => {
    const open = buildTranscript([ev(1, "user", "q"), ev(2, "assistant", "a"), ev(3, "turn_end", "", 1), ev(4, "user", "r"), ev(5, "assistant", "b")]);
    expect(open.filter((i) => i.type === "assistant").map((i) => i.done)).toEqual([true, false]);
    expect(isReplying(open)).toBe(true);

    const ended = buildTranscript([ev(1, "user", "q"), ev(2, "assistant", "a"), ev(3, "turn_end", "", 1), ev(4, "user", "r"), ev(5, "assistant", "b"), ev(6, "turn_end", "", 2)]);
    expect(ended.filter((i) => i.type === "assistant").map((i) => i.done)).toEqual([true, true]);
    expect(isReplying(ended)).toBe(false);
  });
});

describe("repo state notes", () => {
  const repo = (over: Record<string, unknown> = {}) =>
    JSON.stringify({
      branch: "ticket-9-x", head: "abcdef0123456789", dirty: false, dirty_count: 0, upstream: "origin/ticket-9-x",
      ahead: 0, behind: 0, commits: [], pushes: [], changed_paths: [], changed_total: 0, ...over,
    });

  it("says where the repo is, and what the turn did to it", () => {
    const [item] = buildTranscript([
      ev(1, "repo_state", repo({
        commits: [{ sha: "a".repeat(40), subject: "add c" }, { sha: "b".repeat(40), subject: "fix d" }],
        pushes: [{ ref: "origin/ticket-9-x", from: "", to: "a".repeat(40) }],
        dirty: true, dirty_count: 1, ahead: 2, behind: 1,
      })),
    ]);
    expect(item).toEqual({
      type: "note", seq: 1, kind: "repo_state",
      text: "On ticket-9-x at abcdef0, 2 commits this turn (add c; fix d), pushed origin/ticket-9-x, 1 uncommitted change, 2 ahead, 1 behind",
    });
  });

  it("is brief when the turn changed nothing, and says so for a detached head", () => {
    const [quiet, detached] = buildTranscript([ev(1, "repo_state", repo()), ev(2, "repo_state", repo({ branch: "" }))]);
    expect(quiet).toMatchObject({ text: "On ticket-9-x at abcdef0" });
    expect(detached).toMatchObject({ text: "On a detached head at abcdef0" });
  });

  it("does not trip over a body that is not what the host should have sent", () => {
    const items = buildTranscript(
      ["not json", "null", "7", "[]", "{}", '{"commits":[null,{"subject":5}],"pushes":"x","ahead":"1","head":3}'].map((b, i) => ev(i + 1, "repo_state", b)),
    );
    expect(items.map((i) => (i.type === "note" ? i.text : ""))).toEqual([
      "The repo state could not be read.",
      "The repo state could not be read.",
      "The repo state could not be read.",
      "On a detached head at ",
      "On a detached head at ",
      "On a detached head at ",
    ]);
  });

  it("does not end the reply it follows: it comes after the turn's end", () => {
    const items = buildTranscript([ev(1, "user", "q"), ev(2, "assistant", "a", 1), ev(3, "turn_end", "", 1), ev(4, "repo_state", repo(), 1)]);
    expect(items.map((i) => i.type)).toEqual(["user", "assistant", "note"]);
    expect(isReplying(items)).toBe(false);
  });
});


describe("blocks and activity", () => {
  const call = (id: string, status: string, line = "Check pilot-3", tool = "Bash") =>
    JSON.stringify({ call_id: id, tool, line, status });
  const parts = (events: ConversationEvent[]) => {
    const item = buildTranscript(events).find((i) => i.type === "assistant");
    return item?.type === "assistant" ? item.parts : [];
  };

  it("keeps the model's messages apart, and joins the batches of one", () => {
    expect(parts([
      ev(1, "user", "q"),
      ev(2, "assistant", "Checking ", null, "m1"),
      ev(3, "assistant", "pilot-3:", null, "m1"),
      ev(4, "assistant", "Next", null, "m2"),
    ])).toEqual([
      { type: "text", block: "m1", text: "Checking pilot-3:" },
      { type: "text", block: "m2", text: "Next" },
    ]);
  });

  it("runs together text that has no block id, as it did before", () => {
    expect(parts([ev(1, "user", "q"), ev(2, "assistant", "Hel", 1), ev(3, "assistant", "lo", 1)])).toEqual([
      { type: "text", block: null, text: "Hello" },
    ]);
  });

  it("puts a tool call between the texts it fell between, and updates it when it ends", () => {
    const events = [
      ev(1, "user", "q"),
      ev(2, "assistant", "before", null, "m1"),
      ev(3, "activity", call("t1", "running")),
      ev(4, "activity", call("t2", "running", "Read a.go", "Read")),
      ev(5, "activity", call("t1", "failed")),
      ev(6, "assistant", "after", null, "m2"),
    ];
    expect(parts(events)).toEqual([
      { type: "text", block: "m1", text: "before" },
      { type: "activity", calls: [
        { id: "t1", tool: "Bash", line: "Check pilot-3", status: "failed" },
        { id: "t2", tool: "Read", line: "Read a.go", status: "running" },
      ] },
      { type: "text", block: "m2", text: "after" },
    ]);
    expect(currentActivity(buildTranscript(events))).toMatchObject({ id: "t2" });
    expect(currentActivity(buildTranscript(events.slice(0, 5)))).toMatchObject({ id: "t2" });
  });

  it("starts the reply with a tool call when the model used a tool before it said anything", () => {
    const items = buildTranscript([ev(1, "user", "q"), ev(2, "activity", call("t1", "running"))]);
    expect(items.map((i) => i.type)).toEqual(["user", "assistant"]);
    expect(isReplying(items)).toBe(true);
    expect(currentActivity(items)).toMatchObject({ id: "t1", status: "running" });
    expect(turnStartedAt(items)).toBe("2026-01-01T00:00:00Z");
  });

  it("ignores an activity body that is not what the host should have sent", () => {
    const items = buildTranscript([ev(1, "user", "q"), ...["not json", "null", "{}", '{"call_id":1}'].map((b, i) => ev(i + 2, "activity", b))]);
    expect(items.map((i) => i.type)).toEqual(["user"]);
  });
});

describe("runningTurn", () => {
  it("is the turn after the last one that ended", () => {
    expect(runningTurn([])).toBe(1);
    expect(runningTurn([ev(1, "user", "q"), ev(2, "assistant", "a", 1), ev(3, "turn_end", "", 1), ev(4, "user", "q2")])).toBe(2);
    expect(runningTurn([ev(5, "turn_end", "", 4), ev(6, "turn_end", "", 5)])).toBe(6);
  });
});

describe("isStopping", () => {
  it("holds from the request until the turn ends", () => {
    const asked = [ev(1, "user", "q"), ev(2, "interrupt_requested", "", 1)];
    expect(isStopping(asked)).toBe(true);
    expect(isStopping([...asked, ev(3, "turn_end", "", 1)])).toBe(false);
  });

  it("is not about a request for an earlier turn", () => {
    expect(isStopping([ev(1, "turn_end", "", 1), ev(2, "interrupt_requested", "", 1), ev(3, "user", "q2")])).toBe(false);
  });
});
