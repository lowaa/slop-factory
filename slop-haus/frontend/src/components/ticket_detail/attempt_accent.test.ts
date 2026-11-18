import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { ATTEMPT_ACCENT_COUNT, attemptAccentClass, attemptAccentIndex } from "./attempt_accent";

describe("the attempt accent index", () => {
  it.each([
    [1, 1],
    [2, 2],
    [7, 7],
    [8, 1],
    [9, 2],
    [14, 7],
    [15, 1],
  ])("gives attempt %i colour %i", (attempt, index) => {
    expect(attemptAccentIndex(attempt)).toBe(index);
    expect(attemptAccentClass(attempt)).toBe(`attempt-accent-${index}`);
  });

  it("stays inside the palette for a number below 1", () => {
    expect(attemptAccentIndex(0)).toBe(ATTEMPT_ACCENT_COUNT);
    expect(attemptAccentIndex(-3)).toBeGreaterThanOrEqual(1);
  });
});

const INDEX_SCSS = readFileSync(resolve(__dirname, "../../index.scss"), "utf8");

function token(name: string): string {
  const m = INDEX_SCSS.match(new RegExp(`${name}:\\s*(#[0-9a-fA-F]{6})`));
  if (!m) throw new Error(`${name} is not a hex colour in index.scss`);
  return m[1];
}

function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

// WCAG 1.4.11: a graphical object needs 3:1 against what it sits on. The app
// has the one (dark) theme.
describe("the attempt accent tokens", () => {
  it.each([...Array(ATTEMPT_ACCENT_COUNT).keys()].map((i) => i + 1))(
    "--attempt-%i has 3:1 contrast against the page and a raised surface",
    (n) => {
      const accent = token(`--attempt-${n}`);
      expect(contrast(accent, token("--color-bg"))).toBeGreaterThanOrEqual(3);
      expect(contrast(accent, token("--color-bg-raised"))).toBeGreaterThanOrEqual(3);
    },
  );
});

const ACCENT_SCSS = readFileSync(resolve(__dirname, "attempt_accent.scss"), "utf8");

describe("the accent classes", () => {
  it.each([...Array(ATTEMPT_ACCENT_COUNT).keys()].map((i) => i + 1))(
    ".attempt-accent-%i sets the accent to its own token",
    (n) => {
      const rule = ACCENT_SCSS.match(new RegExp(`\\.attempt-accent-${n}\\s*\\{([^}]*)\\}`));
      expect(rule?.[1].replace(/\s+/g, "")).toBe(`--attempt-accent:var(--attempt-${n});`);
    },
  );

  it("has no class beyond the palette", () => {
    expect(ACCENT_SCSS.match(/\.attempt-accent-\d+/g)).toHaveLength(ATTEMPT_ACCENT_COUNT);
  });
});
