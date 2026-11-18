import { roundVisibleMs, VISIBLE_MS_MAX, type Screen } from "./catalog";
import type { Usage } from "./usage";

// Time on a screen, counted only while the page is visible and the window
// focused. There is no per-second stream: the time is sent when the screen is
// left, and when the page is hidden or closed (which may be the last chance),
// so a visit is usually one event and a few if the person switched away and
// back.
export class ScreenTracker {
  private readonly usage: Usage;
  private readonly now: () => number;
  private screen: Screen | null = null;
  private visible = false;
  // When the current visible stretch began, if one is running.
  private since: number | null = null;
  // Visible time not yet sent, and what rounding the last event added or took
  // off: carried so that many short stretches don't each round down to nothing.
  private pendingMs = 0;
  private carryMs = 0;

  constructor(usage: Usage, now: () => number) {
    this.usage = usage;
    this.now = now;
  }

  // The page's visibility, as the page has it: visible and focused.
  setVisible(visible: boolean): void {
    if (visible === this.visible) return;
    this.visible = visible;
    if (visible) this.startClock();
    else this.stopClock();
  }

  // The person is now on `screen`, or on none (null). Staying on the same one
  // (a search param changed) is not a new visit.
  enter(screen: Screen | null): void {
    if (screen === this.screen) return;
    this.finishVisit();
    this.screen = screen;
    this.usage.setScreen(screen);
    if (screen !== null) this.usage.track("screen_view", { screen });
    this.startClock();
  }

  // The page is being hidden or closed: send what has been counted, and push
  // it out before the page goes.
  pageHidden(): void {
    this.setVisible(false);
    this.sendPending();
    this.usage.flush();
  }

  private startClock(): void {
    if (this.screen !== null && this.visible && this.since === null) this.since = this.now();
  }

  private finishVisit(): void {
    this.stopClock();
    this.sendPending();
    this.carryMs = 0;
  }

  private stopClock(): void {
    if (this.since !== null) this.pendingMs += this.now() - this.since;
    this.since = null;
  }

  private sendPending(): void {
    if (this.screen === null || this.pendingMs <= 0) {
      this.pendingMs = 0;
      return;
    }
    const total = this.pendingMs + this.carryMs;
    const visible = roundVisibleMs(total);
    // A capped value is an idle tab's: its excess is not carried into the next.
    this.carryMs = visible >= VISIBLE_MS_MAX ? 0 : total - visible;
    this.pendingMs = 0;
    this.usage.track("screen_time", { screen: this.screen, visible_ms: visible });
  }
}
