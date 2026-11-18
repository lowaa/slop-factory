import { readdirSync, readFileSync } from "node:fs";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ScreenTracker } from "./screen_time";
import { Usage } from "./usage";

// Usage is counted in memory and sent; nothing about it may stay on the
// device, which is what lets it run without a consent prompt.
describe("the usage module and web storage", () => {
  afterEach(() => vi.restoreAllMocks());

  it("never reads or writes web storage, cookies or IndexedDB in a session", () => {
    const calls = [
      vi.spyOn(Storage.prototype, "setItem"),
      vi.spyOn(Storage.prototype, "getItem"),
      vi.spyOn(Storage.prototype, "removeItem"),
      vi.spyOn(Storage.prototype, "clear"),
      vi.spyOn(document, "cookie", "set"),
    ];
    const usage = new Usage(() => {}, () => true);
    let now = 0;
    const tracker = new ScreenTracker(usage, () => now);
    tracker.setVisible(true);
    tracker.enter("board");
    usage.event("new_ticket", { action: "open_form" });
    now += 30_000;
    tracker.enter("reports");
    tracker.pageHidden();
    for (const spy of calls) expect(spy).not.toHaveBeenCalled();
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });

  it("does not mention storage in its sources", () => {
    const sources = readdirSync(import.meta.dirname).filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f));
    expect(sources.length).toBeGreaterThan(0);
    for (const file of sources) {
      const text = readFileSync(`${import.meta.dirname}/${file}`, "utf8");
      expect(text, file).not.toMatch(/localStorage|sessionStorage|indexedDB|document\.cookie|browserLocalStore|browserSessionStore|@slop-factory\/storage/);
    }
  });
});
