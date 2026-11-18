import { describe, expect, it } from "vitest";
import type { LandingEntry } from "@/api";
import { describeLanding } from "./describe_landing";

const base: LandingEntry = {
  ticket_id: 7,
  attempt_number: 1,
  change_url: "",
  state: "landing",
  position: 2,
  step: "",
  back_reason: "",
  batch_id: null,
  batch_position: 0,
  since_update_seconds: 0,
};

describe("describeLanding", () => {
  it("shows the queue position of a landing that has not begun", () => {
    expect(describeLanding(base).text).toBe("landing · #2 in queue");
  });

  it.each([
    ["merging_main", "landing · merging main"],
    ["waiting_checks", "landing · waiting for CI"],
    ["merging", "landing · merging"],
  ] as const)("shows the step %s of a landing that has begun", (step, text) => {
    expect(describeLanding({ ...base, position: 1, step }).text).toBe(text);
  });

  it("shows the batch a change is tested in and its place in the merge order", () => {
    const label = describeLanding({ ...base, step: "waiting_checks", position: 1, batch_id: 12, batch_position: 2 });
    expect(label.text).toBe("landing · waiting for CI · batch 12 #2");
    expect(label.title).toContain("merged 2nd of them");
  });

  it("says nothing of batches outside one", () => {
    expect(describeLanding(base).text).not.toContain("batch");
  });

  it("says why a change was handed back", () => {
    const label = describeLanding({ ...base, state: "back_with_agent", position: 0, back_reason: "ci_red" });
    expect(label.text).toBe("back with agent");
    expect(label.tone).toBe("back");
    expect(label.title).toContain("ci_red");
  });
});

describe("describeLanding time in step", () => {
  it("shows how long the landing has been in its step", () => {
    expect(describeLanding({ ...base, step: "merging", since_update_seconds: 125 }).since).toBe("2m 5s");
  });

  it("calls a merge step past the server's bound stalled, and a CI wait never", () => {
    const late = 16 * 60;
    expect(describeLanding({ ...base, step: "merging", since_update_seconds: late }).stalled).toBe(true);
    expect(describeLanding({ ...base, step: "merging_main", since_update_seconds: late }).stalled).toBe(true);
    expect(describeLanding({ ...base, step: "merging", since_update_seconds: 60 }).stalled).toBe(false);
    expect(describeLanding({ ...base, step: "waiting_checks", since_update_seconds: late * 10 }).stalled).toBe(false);
  });
});
