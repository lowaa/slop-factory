import { onTestFinished, vi } from "vitest";
import { COARSE_POINTER_QUERY, NARROW_QUERY } from "@/lib/layout";

// stubNarrowLayout makes the window phone-width for this test, as far as
// lib/layout.ts can tell: jsdom has no matchMedia (and no layout) of its own.
export function stubNarrowLayout(): void {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query === NARROW_QUERY,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
  onTestFinished(() => {
    vi.unstubAllGlobals();
  });
}

// stubTouchDevice makes touch the primary input for this test, and returns a
// way to change that mid-test (a keyboard docked to a tablet, say).
export function stubTouchDevice(touch = true): { setTouch: (next: boolean) => void } {
  let matches = touch;
  const listeners = new Set<() => void>();
  vi.stubGlobal("matchMedia", (query: string) => ({
    get matches() {
      return query === COARSE_POINTER_QUERY && matches;
    },
    media: query,
    addEventListener: (_type: string, fn: () => void) => listeners.add(fn),
    removeEventListener: (_type: string, fn: () => void) => listeners.delete(fn),
  }));
  onTestFinished(() => {
    vi.unstubAllGlobals();
  });
  return {
    setTouch: (next) => {
      matches = next;
      listeners.forEach((fn) => fn());
    },
  };
}
