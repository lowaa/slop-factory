import { describe, expect, it } from "vitest";
import { DAY_MS, MINUTE_MS, expiresIn } from "./format_time";

const NOW = Date.parse("2026-01-01T00:00:00Z");
const at = (ms: number) => new Date(NOW + ms).toISOString();

describe("expiresIn", () => {
  it.each([
    [at(5 * MINUTE_MS), "minute", "expires in 5m"],
    [at(1000), "minute", "expires in 1m"],
    [at(3 * DAY_MS), "day", "expires in 3d"],
    [at(60 * MINUTE_MS), "day", "expires in 1d"],
    [at(0), "minute", "expired"],
    [at(-DAY_MS), "day", "expired"],
  ] as const)("%s in %ss is %s", (iso, unit, want) => {
    expect(expiresIn(iso, NOW, unit)).toBe(want);
  });
});
