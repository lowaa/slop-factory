// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BuildTag } from "./build_tag";
import { UpdateWatcher } from "./update_watcher";

const INTERVAL = 1000;

function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", { value: state, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

async function setup(initialServer = "b1") {
  let server = initialServer;
  const fetchTag = vi.fn(async () => {
    if (server === "FAIL") throw new Error("down");
    return server;
  });
  // BuildTag reads /version.json through fetch.
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ version: "b1" }))));
  const buildTag = new BuildTag();
  await buildTag.get();
  const watcher = new UpdateWatcher({
    buildTag,
    intervalMs: INTERVAL,
    fetchTag: async () => {
      try {
        return await fetchTag();
      } catch {
        return "unknown";
      }
    },
  });
  return { watcher, buildTag, fetchTag, deploy: (tag: string) => (server = tag) };
}

beforeEach(() => {
  vi.useFakeTimers();
  setVisibility("visible");
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("UpdateWatcher", () => {
  it("offers the new tag when the server's differs, and records it on the build tag", async () => {
    const { watcher, buildTag, deploy } = await setup();
    const stop = watcher.start();
    deploy("b2");
    await vi.advanceTimersByTimeAsync(INTERVAL);
    expect(watcher.getSnapshot()).toBe("b2");
    expect(buildTag.serverTag()).toBe("b2");
    stop();
  });

  it("says nothing while the tag is the same", async () => {
    const { watcher, buildTag } = await setup();
    const stop = watcher.start();
    await vi.advanceTimersByTimeAsync(INTERVAL * 3);
    expect(watcher.getSnapshot()).toBeUndefined();
    expect(buildTag.serverTag()).toBeUndefined();
    stop();
  });

  it("stays dismissed for that tag, and returns for a newer one", async () => {
    const { watcher, deploy } = await setup();
    const stop = watcher.start();
    deploy("b2");
    await vi.advanceTimersByTimeAsync(INTERVAL);
    watcher.dismiss();
    expect(watcher.getSnapshot()).toBeUndefined();
    await vi.advanceTimersByTimeAsync(INTERVAL * 2);
    expect(watcher.getSnapshot()).toBeUndefined();
    deploy("b3");
    await vi.advanceTimersByTimeAsync(INTERVAL);
    expect(watcher.getSnapshot()).toBe("b3");
    stop();
  });

  it("survives a version.json failure", async () => {
    const { watcher, deploy } = await setup();
    const stop = watcher.start();
    deploy("FAIL");
    await vi.advanceTimersByTimeAsync(INTERVAL);
    expect(watcher.getSnapshot()).toBeUndefined();
    deploy("b2");
    await vi.advanceTimersByTimeAsync(INTERVAL);
    expect(watcher.getSnapshot()).toBe("b2");
    stop();
  });

  it("polls only while visible, and checks at once on becoming visible", async () => {
    const { watcher, fetchTag } = await setup();
    const stop = watcher.start();
    setVisibility("hidden");
    await vi.advanceTimersByTimeAsync(INTERVAL * 5);
    expect(fetchTag).not.toHaveBeenCalled();
    setVisibility("visible");
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchTag).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(INTERVAL);
    expect(fetchTag).toHaveBeenCalledTimes(2);
    stop();
  });

  it("stops polling when stopped", async () => {
    const { watcher, fetchTag } = await setup();
    watcher.start()();
    await vi.advanceTimersByTimeAsync(INTERVAL * 3);
    expect(fetchTag).not.toHaveBeenCalled();
  });
});
