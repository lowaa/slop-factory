export type PlacementPreference = "auto" | "bottom" | "top";
export type PanelSide = "bottom" | "top";

// The panel's own CSS ceiling; the available space can only lower it.
export const PANEL_MAX_HEIGHT = 260;

export interface PlacementInput {
  preference: PlacementPreference;
  // Free pixels between the anchor and the boundary on each side.
  spaceBelow: number;
  spaceAbove: number;
  // The panel's height uncapped by max-height, borders included.
  contentHeight: number;
  maxHeight?: number;
}

export interface PlacementResult {
  side: PanelSide;
  maxHeight: number;
}

// Pure so the decision is unit-testable: jsdom has no layout, so none of
// the measuring can be exercised there.
export function decidePlacement({
  preference,
  spaceBelow,
  spaceAbove,
  contentHeight,
  maxHeight = PANEL_MAX_HEIGHT,
}: PlacementInput): PlacementResult {
  const wanted = Math.min(contentHeight, maxHeight);
  let side: PanelSide;
  if (preference !== "auto") side = preference;
  else if (spaceBelow >= wanted) side = "bottom";
  else if (spaceAbove >= wanted) side = "top";
  // Neither fits whole: take the roomier side and scroll inside it.
  else side = spaceAbove > spaceBelow ? "top" : "bottom";

  const space = side === "bottom" ? spaceBelow : spaceAbove;
  return { side, maxHeight: Math.max(0, Math.min(maxHeight, Math.floor(space))) };
}

export interface Edges {
  top: number;
  bottom: number;
}

// Overflow other than `visible` clips an absolutely positioned child, so
// such ancestors bound the panel as much as the viewport does.
function isClipping(el: Element): boolean {
  const { overflowY } = getComputedStyle(el);
  return overflowY !== "visible";
}

export function visibleBounds(from: Element): Edges {
  const root = document.documentElement;
  let top = 0;
  let bottom = window.innerHeight || root.clientHeight;
  for (let el = from.parentElement; el && el !== document.body && el !== root; el = el.parentElement) {
    if (!isClipping(el)) continue;
    const rect = el.getBoundingClientRect();
    top = Math.max(top, rect.top);
    bottom = Math.min(bottom, rect.bottom);
  }
  return { top, bottom };
}
