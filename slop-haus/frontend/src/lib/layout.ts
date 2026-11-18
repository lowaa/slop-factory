import { useSyncExternalStore } from "react";
import { createBreakpoints } from "@slop-factory/ui/styles";

// The same widths as styles/_layout.scss; layout.test.ts fails if they differ.
export const BREAKPOINT_WIDTHS = {
  narrow: 720,
  hint: 900,
  rows: 600,
  "header-names": 1536,
} as const;

export const breakpoints = createBreakpoints(BREAKPOINT_WIDTHS);

// NARROW_QUERY is the phone layout. Most of it is styling alone; this is for
// the parts that are different markup (the header's menu), which CSS cannot
// switch.
export const NARROW_QUERY = breakpoints.query("narrow", "down");

function narrowQuery(): MediaQueryList | null {
  // jsdom has no matchMedia; without one there is no layout to adapt to,
  // so it gets the desktop markup.
  return typeof window.matchMedia === "function" ? window.matchMedia(NARROW_QUERY) : null;
}

function subscribe(onChange: () => void): () => void {
  const query = narrowQuery();
  query?.addEventListener("change", onChange);
  return () => query?.removeEventListener("change", onChange);
}

function isNarrow(): boolean {
  return narrowQuery()?.matches ?? false;
}

// useNarrowLayout is whether the window is phone-width, following resizes
// and rotation.
export function useNarrowLayout(): boolean {
  return useSyncExternalStore(subscribe, isNarrow);
}

// COARSE_POINTER_QUERY is a touch screen as the primary input. Input
// capability rather than width or user agent: a narrow desktop window still
// has a keyboard, and a tablet with one reports whatever its primary input is.
export const COARSE_POINTER_QUERY = "(pointer: coarse)";

function coarseQuery(): MediaQueryList | null {
  return typeof window.matchMedia === "function" ? window.matchMedia(COARSE_POINTER_QUERY) : null;
}

function subscribeCoarse(onChange: () => void): () => void {
  const query = coarseQuery();
  query?.addEventListener("change", onChange);
  return () => query?.removeEventListener("change", onChange);
}

// useCoarsePointer is whether the primary input is touch, following changes
// (a keyboard docked to a tablet, say).
export function useCoarsePointer(): boolean {
  return useSyncExternalStore(subscribeCoarse, () => coarseQuery()?.matches ?? false);
}
