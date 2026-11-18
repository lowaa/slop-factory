import { describe, expect, it, vi } from "vitest";
import { ScriptGate, type ConsentSource } from "./script_gate";

// The gate's side of the consent store, reduced to what it reads: which
// purposes are granted, and a way to tell it that changed.
class FakeSource implements ConsentSource {
  private granted = new Set<string>();
  private listeners = new Set<() => void>();
  isGranted = (id: string) => this.granted.has(id);
  subscribe = (l: () => void) => {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  };
  set(id: string, on: boolean) {
    if (on) this.granted.add(id);
    else this.granted.delete(id);
    for (const l of [...this.listeners]) l();
  }
}

// A loader the test finishes by hand, so a withdrawal can land mid-load.
function deferredLoader() {
  const teardown = vi.fn();
  let finish: () => void = () => undefined;
  let signal: AbortSignal | undefined;
  const loader = vi.fn(
    (s: AbortSignal) =>
      new Promise<() => void>((resolve) => {
        signal = s;
        finish = () => resolve(teardown);
      }),
  );
  return { loader, teardown, finish: () => finish(), signal: () => signal };
}

const flush = () => new Promise((r) => setTimeout(r, 0));

describe("the script gate", () => {
  it("never loads while the purpose is not granted", async () => {
    const source = new FakeSource();
    const loader = vi.fn();
    new ScriptGate(source).loadWhenConsented("analytics", loader);
    source.set("marketing", true);
    await flush();
    expect(loader).not.toHaveBeenCalled();
  });

  it("loads once the purpose is granted, and only once", async () => {
    const source = new FakeSource();
    const loader = vi.fn();
    new ScriptGate(source).loadWhenConsented("analytics", loader);
    source.set("analytics", true);
    source.set("analytics", true);
    await flush();
    expect(loader).toHaveBeenCalledTimes(1);
  });

  it("loads straight away when consent was already given", async () => {
    const source = new FakeSource();
    source.set("analytics", true);
    const loader = vi.fn();
    new ScriptGate(source).loadWhenConsented("analytics", loader);
    await flush();
    expect(loader).toHaveBeenCalledTimes(1);
  });

  it("tears the script down on withdrawal and loads again if it is granted again", async () => {
    const source = new FakeSource();
    const teardown = vi.fn();
    const loader = vi.fn(async () => teardown);
    new ScriptGate(source).loadWhenConsented("analytics", loader);
    source.set("analytics", true);
    await flush();
    source.set("analytics", false);
    expect(teardown).toHaveBeenCalledTimes(1);
    source.set("analytics", false);
    expect(teardown).toHaveBeenCalledTimes(1);
    source.set("analytics", true);
    await flush();
    expect(loader).toHaveBeenCalledTimes(2);
  });

  it("tears down a script that finished loading after consent was withdrawn, and aborts the load", async () => {
    const source = new FakeSource();
    const d = deferredLoader();
    new ScriptGate(source).loadWhenConsented("analytics", d.loader);
    source.set("analytics", true);
    source.set("analytics", false);
    expect(d.signal()?.aborted).toBe(true);
    d.finish();
    await flush();
    expect(d.teardown).toHaveBeenCalledTimes(1);
  });

  it("tears down a script that finished loading after the caller went away", async () => {
    const source = new FakeSource();
    const d = deferredLoader();
    const stop = new ScriptGate(source).loadWhenConsented("analytics", d.loader);
    source.set("analytics", true);
    stop();
    d.finish();
    await flush();
    expect(d.teardown).toHaveBeenCalledTimes(1);
  });

  it("does not load when consent arrives after the caller went away", async () => {
    const source = new FakeSource();
    const loader = vi.fn();
    const stop = new ScriptGate(source).loadWhenConsented("analytics", loader);
    stop();
    source.set("analytics", true);
    await flush();
    expect(loader).not.toHaveBeenCalled();
  });

  it("loads a script mounted twice under one id once, and tears it down with the last holder", async () => {
    const source = new FakeSource();
    const teardown = vi.fn();
    const loader = vi.fn(async () => teardown);
    const gate = new ScriptGate(source);
    const stopA = gate.loadWhenConsented("analytics", loader, { id: "recorder" });
    const stopB = gate.loadWhenConsented("analytics", loader, { id: "recorder" });
    source.set("analytics", true);
    await flush();
    expect(loader).toHaveBeenCalledTimes(1);
    stopA();
    stopA();
    expect(teardown).not.toHaveBeenCalled();
    stopB();
    expect(teardown).toHaveBeenCalledTimes(1);
  });

  it("refuses to share an id between two purposes", () => {
    const gate = new ScriptGate(new FakeSource());
    gate.loadWhenConsented("analytics", vi.fn(), { id: "x" });
    expect(() => gate.loadWhenConsented("marketing", vi.fn(), { id: "x" })).toThrow();
  });

  it("reports a failing loader or teardown without breaking the page", async () => {
    const source = new FakeSource();
    const onError = vi.fn();
    const gate = new ScriptGate(source);
    gate.loadWhenConsented("analytics", () => Promise.reject(new Error("vendor down")), { onError });
    source.set("analytics", true);
    await flush();
    expect(onError).toHaveBeenCalledTimes(1);
    source.set("other", true);
    await flush();
    expect(onError).toHaveBeenCalledTimes(1);

    gate.loadWhenConsented(
      "marketing",
      () => () => {
        throw new Error("teardown broke");
      },
      { onError },
    );
    source.set("marketing", true);
    await flush();
    source.set("marketing", false);
    expect(onError).toHaveBeenCalledTimes(2);
  });
});
