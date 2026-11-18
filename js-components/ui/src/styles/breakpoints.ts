import { useSyncExternalStore } from "react";

export type BreakpointDirection = "down" | "up";

// The same ranges as _layout.scss's mixins, so a query here and an @include
// there match at every width.
export function breakpointQuery(width: number, direction: BreakpointDirection): string {
  return direction === "down" ? `(width <= ${width}px)` : `(width >= ${width}px)`;
}

export function breakpointRangeQuery(from: number, to: number): string {
  return `(width >= ${from}px) and (width <= ${to}px)`;
}

function matchMediaOrNull(query: string): MediaQueryList | null {
  // jsdom has no matchMedia; without one there is no layout to adapt to,
  // so a query does not match and the desktop markup renders.
  return typeof window !== "undefined" && typeof window.matchMedia === "function" ? window.matchMedia(query) : null;
}

// useMediaQuery is whether the query matches, following resizes and
// rotation.
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const list = matchMediaOrNull(query);
      list?.addEventListener("change", onChange);
      return () => list?.removeEventListener("change", onChange);
    },
    () => matchMediaOrNull(query)?.matches ?? false,
    () => false,
  );
}

export interface Breakpoints<Name extends string> {
  widths: Readonly<Record<Name, number>>;
  query(name: Name, direction: BreakpointDirection): string;
  rangeQuery(from: Name, to: Name): string;
  useMatches(name: Name, direction: BreakpointDirection): boolean;
}

// createBreakpoints is an app's named widths in TS. Pass the same map the
// app gives `@use ... with ($breakpoints: ...)`, in px.
export function createBreakpoints<Name extends string>(
  widths: Readonly<Record<Name, number>>,
): Breakpoints<Name> {
  const query = (name: Name, direction: BreakpointDirection) =>
    breakpointQuery(widths[name], direction);
  return {
    widths,
    query,
    rangeQuery: (from, to) => breakpointRangeQuery(widths[from], widths[to]),
    useMatches: (name, direction) => useMediaQuery(query(name, direction)),
  };
}
