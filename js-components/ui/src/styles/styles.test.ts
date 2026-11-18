import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { compile, compileString } from "sass";
import { pathToFileURL } from "node:url";
import { join } from "node:path";
import { createBreakpoints, useMediaQuery } from "./breakpoints";
import { readScssBreakpoints, uiStylesImporter } from "./scss_breakpoints";

const APP_LAYOUT = join(import.meta.dirname, "testdata/_app_layout.scss");
const Z_INDEX = join(import.meta.dirname, "_z_index.scss");

const WIDTHS = { narrow: 720, desktop: 769, wide: 1000, laptop: 1199 } as const;

function compileWithApp(body: string): string {
  return compileString(
    `@use "./_app_layout"; @use "@slop-factory/ui/styles/layout" as l; ${body}`,
    { url: pathToFileURL(join(import.meta.dirname, "testdata/probe.scss")), importers: [uiStylesImporter] },
  ).css.replace(/\s+/g, " ");
}

describe("layout mixins", () => {
  it("emits range syntax for down, up and between", () => {
    const css = compileWithApp(`
      .a { @include l.down(narrow) { x: 1; } }
      .b { @include l.up(wide) { x: 2; } }
      .c { @include l.between(desktop, laptop) { x: 3; } }
      .d { @include l.up(desktop, $min-height: 560px) { x: 4; } }`);
    expect(css).toContain("@media (width <= 720px) { .a { x: 1; } }");
    expect(css).toContain("@media (width >= 1000px) { .b { x: 2; } }");
    expect(css).toContain("@media (width >= 769px) and (width <= 1199px) { .c { x: 3; } }");
    expect(css).toContain("@media (width >= 769px) and (height >= 560px) { .d { x: 4; } }");
  });

  it("refuses a breakpoint the app did not define", () => {
    expect(() => compileWithApp(".a { @include l.down(tiny) { x: 1; } }")).toThrow(/Unknown breakpoint `tiny`/);
  });
});

describe("breakpoint TS helper", () => {
  const bp = createBreakpoints(WIDTHS);

  it("builds the same queries the mixins emit", () => {
    expect(bp.query("narrow", "down")).toBe("(width <= 720px)");
    expect(bp.query("wide", "up")).toBe("(width >= 1000px)");
    expect(bp.rangeQuery("desktop", "laptop")).toBe("(width >= 769px) and (width <= 1199px)");
  });

  it("does not match, rather than throw, where matchMedia is missing (jsdom)", () => {
    const { result } = renderHook(() => bp.useMatches("narrow", "down"));
    expect(result.current).toBe(false);
  });

  it("follows matchMedia changes", () => {
    const original = window.matchMedia;
    let matches = false;
    const listeners = new Set<() => void>();
    window.matchMedia = ((query: string) => ({
      get matches() {
        return query === "(width <= 720px)" && matches;
      },
      addEventListener: (_: string, fn: () => void) => listeners.add(fn),
      removeEventListener: (_: string, fn: () => void) => listeners.delete(fn),
    })) as unknown as typeof window.matchMedia;
    try {
      const { result, unmount } = renderHook(() => useMediaQuery("(width <= 720px)"));
      expect(result.current).toBe(false);
      matches = true;
      act(() => listeners.forEach((fn) => fn()));
      expect(result.current).toBe(true);
      unmount();
      expect(listeners.size).toBe(0);
    } finally {
      window.matchMedia = original;
    }
  });
});

describe("SCSS and TS breakpoints", () => {
  it("agree on every name and width", () => {
    expect(readScssBreakpoints(APP_LAYOUT)).toEqual(WIDTHS);
  });

  it("reads each app's own values, so a drifted map is caught", () => {
    const other = readScssBreakpoints(join(import.meta.dirname, "testdata/_other_app_layout.scss"));
    expect(other).toEqual({ narrow: 721 });
    expect(other).not.toEqual({ narrow: WIDTHS.narrow });
  });
});

describe("z-index scale", () => {
  const css = compile(Z_INDEX).css;
  const tokens = Object.fromEntries(
    [...css.matchAll(/--z-([\w-]+):\s*(\d+);/g)].map(([, name, value]) => [name, Number(value)]),
  );

  it("defines every layer on :root", () => {
    expect(css).toMatch(/^:root \{/);
    expect(Object.keys(tokens)).toEqual([
      "base", "sticky", "dropdown", "menu", "scrim", "drawer", "overlay", "toast", "modal",
    ]);
  });

  it("keeps the values the audited components use today", () => {
    expect(tokens).toEqual({
      base: 1, sticky: 10, dropdown: 10, menu: 20, scrim: 20, drawer: 30, overlay: 100, toast: 900, modal: 1000,
    });
  });

  it("keeps the layers in ascending order", () => {
    const values = Object.values(tokens);
    expect(values).toEqual([...values].sort((a, b) => a - b));
  });

  it("keeps the banner below the dialog that opens from it", () => {
    expect(tokens.toast).toBeLessThan(tokens.modal);
  });
});
