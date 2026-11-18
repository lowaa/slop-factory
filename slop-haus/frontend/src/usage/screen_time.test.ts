import { beforeEach, describe, expect, it } from "vitest";
import { RecordingUsage } from "./recording_usage";
import { ScreenTracker } from "./screen_time";

// The clock is the test's: nothing waits for real time.
let nowMs: number;
const advance = (ms: number) => void (nowMs += ms);
let usage: RecordingUsage;
let tracker: ScreenTracker;

beforeEach(() => {
  nowMs = 1_000_000;
  usage = new RecordingUsage();
  tracker = new ScreenTracker(usage, () => nowMs);
});

const times = () => usage.named("screen_time").map((a) => [a.screen, a.visible_ms]);

describe("ScreenTracker", () => {
  it("reports the screen on entering it, and once per move rather than per search change", () => {
    tracker.enter("board");
    tracker.enter("board");
    tracker.enter("reports");
    expect(usage.named("screen_view")).toEqual([{ screen: "board" }, { screen: "reports" }]);
  });

  it("sends the visible time once, when the screen is left, rounded to 5 s", () => {
    tracker.setVisible(true);
    tracker.enter("board");
    advance(12_400);
    expect(times()).toEqual([]);

    tracker.enter("reports");
    expect(times()).toEqual([["board", 10_000]]);
  });

  it("counts only visible time: a hidden or unfocused stretch adds nothing", () => {
    tracker.setVisible(true);
    tracker.enter("board");
    advance(10_000);
    tracker.setVisible(false);
    advance(600_000);
    tracker.setVisible(true);
    advance(5_000);
    tracker.enter("reports");
    expect(times()).toEqual([["board", 15_000]]);
  });

  it("counts nothing for a screen entered while the page is not visible, until it is", () => {
    tracker.enter("board");
    advance(60_000);
    tracker.setVisible(true);
    advance(5_000);
    tracker.enter("reports");
    expect(times()).toEqual([["board", 5_000]]);
  });

  it("sends nothing for a screen that was never visible", () => {
    tracker.enter("board");
    advance(60_000);
    tracker.enter("reports");
    expect(times()).toEqual([]);
  });

  it("caps a visit at 30 minutes, so an idle tab is one outlier and not a skew", () => {
    tracker.setVisible(true);
    tracker.enter("board");
    advance(6 * 60 * 60 * 1000);
    tracker.enter("reports");
    expect(times()).toEqual([["board", 30 * 60 * 1000]]);
  });

  it("sends what has been counted when the page is hidden, and pushes it out", () => {
    tracker.setVisible(true);
    tracker.enter("board");
    advance(20_000);

    tracker.pageHidden();

    expect(times()).toEqual([["board", 20_000]]);
    expect(usage.flushes).toBe(1);
  });

  it("resumes on visible, and the same visit's later time is its own event", () => {
    tracker.setVisible(true);
    tracker.enter("board");
    advance(20_000);
    tracker.pageHidden();
    advance(300_000);
    tracker.setVisible(true);
    advance(10_000);
    tracker.enter("reports");

    expect(times()).toEqual([
      ["board", 20_000],
      ["board", 10_000],
    ]);
  });

  it("does not lose short stretches to rounding: each rounds on top of what the last left over", () => {
    tracker.setVisible(true);
    tracker.enter("board");
    for (let i = 0; i < 4; i++) {
      advance(1_000);
      tracker.pageHidden();
      tracker.setVisible(true);
    }
    tracker.enter("reports");
    const total = usage.named("screen_time").reduce((sum, a) => sum + (a.visible_ms as number), 0);
    expect(total).toBe(5_000);
  });

  it("keeps the whole visit within half a step of what was counted, however the stretches fall", () => {
    tracker.setVisible(true);
    tracker.enter("board");
    for (const stretch of [2_500, 1_000, 4_000, 700, 3_300]) {
      advance(stretch);
      tracker.pageHidden();
      tracker.setVisible(true);
    }
    tracker.enter("reports");
    const reported = usage.named("screen_time").reduce((sum, a) => sum + (a.visible_ms as number), 0);
    expect(Math.abs(reported - 11_500)).toBeLessThanOrEqual(2_500);
  });

  it("starts a new visit's rounding afresh", () => {
    tracker.setVisible(true);
    tracker.enter("board");
    advance(2_000);
    tracker.enter("reports");
    advance(2_000);
    tracker.enter("board");
    expect(times()).toEqual([
      ["board", 0],
      ["reports", 0],
    ]);
  });

  it("sends nothing when the page goes away off any screen", () => {
    tracker.setVisible(true);
    tracker.pageHidden();
    expect(times()).toEqual([]);
    expect(usage.events).toEqual([]);
  });

  it("sets the screen on the usage it reports through, and clears it on leaving", () => {
    const t = new ScreenTracker(usage, () => nowMs);
    t.enter("board");
    expect(usage.currentScreen).toBe("board");
    t.enter(null);
    expect(usage.currentScreen).toBeNull();
  });
});
