import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { watchPage } from "./page_lifecycle";
import { RecordingUsage } from "./recording_usage";
import { ScreenTracker } from "./screen_time";

let nowMs = 0;
let state: { visibility: DocumentVisibilityState; focused: boolean };
let usage: RecordingUsage;
let tracker: ScreenTracker;
let stop: () => void;

function setPage(next: Partial<typeof state>, fire: () => void) {
  Object.assign(state, next);
  fire();
}

beforeEach(() => {
  nowMs = 0;
  state = { visibility: "visible", focused: true };
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => state.visibility);
  vi.spyOn(document, "hasFocus").mockImplementation(() => state.focused);
  usage = new RecordingUsage();
  tracker = new ScreenTracker(usage, () => nowMs);
  stop = watchPage(tracker, window, document);
  tracker.enter("board");
});

afterEach(() => {
  stop();
  vi.restoreAllMocks();
});

const boardMs = () => usage.named("screen_time").map((a) => a.visible_ms);

describe("watchPage", () => {
  it("stops the clock when the window loses focus and restarts it on focus", () => {
    nowMs += 10_000;
    setPage({ focused: false }, () => window.dispatchEvent(new Event("blur")));
    nowMs += 100_000;
    setPage({ focused: true }, () => window.dispatchEvent(new Event("focus")));
    nowMs += 5_000;
    tracker.enter(null);
    expect(boardMs()).toEqual([15_000]);
  });

  it("flushes when the tab is hidden, and resumes when it is shown", () => {
    nowMs += 10_000;
    setPage({ visibility: "hidden", focused: false }, () => document.dispatchEvent(new Event("visibilitychange")));
    expect(boardMs()).toEqual([10_000]);
    expect(usage.flushes).toBe(1);

    nowMs += 500_000;
    setPage({ visibility: "visible", focused: true }, () => document.dispatchEvent(new Event("visibilitychange")));
    nowMs += 5_000;
    tracker.enter(null);
    expect(boardMs()).toEqual([10_000, 5_000]);
  });

  it("flushes on pagehide, which a closing tab may fire without a visibility change", () => {
    nowMs += 10_000;
    window.dispatchEvent(new Event("pagehide"));
    expect(boardMs()).toEqual([10_000]);
    expect(usage.flushes).toBe(1);
  });

  it("resumes when the page is restored from the back/forward cache", () => {
    nowMs += 10_000;
    window.dispatchEvent(new Event("pagehide"));
    setPage({ visibility: "hidden", focused: false }, () => {});
    nowMs += 3_600_000;
    setPage({ visibility: "visible", focused: true }, () => window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true })));
    nowMs += 5_000;
    tracker.enter(null);
    expect(boardMs()).toEqual([10_000, 5_000]);
  });

  it("stops listening when told to", () => {
    stop();
    nowMs += 10_000;
    window.dispatchEvent(new Event("pagehide"));
    expect(boardMs()).toEqual([]);
  });
});
