import { describe, expect, it } from "vitest";
import { CATALOG, VISIBLE_MS_MAX, countBucket, roundVisibleMs } from "./catalog";

const attributes = [...new Set(Object.values(CATALOG).flatMap((attrs) => Object.keys(attrs)))].sort();

describe("the catalog", () => {
  it("has no attribute that could identify a person, a tenant, an agent or a session", () => {
    const identifying = /(^|[._])(user|tenant|account|agent|session|email|id|ids|name|names|url|ticket)([._]|$)/;
    expect(attributes.filter((a) => identifying.test(a))).toEqual([]);
  });

  it("allows only enums, flags and bounded numbers, so no attribute can carry free text", () => {
    for (const attrs of Object.values(CATALOG)) {
      for (const spec of Object.values(attrs) as { type: string; max?: number; step?: number }[]) {
        expect(["enum", "bool", "int"]).toContain(spec.type);
        if (spec.type === "int") {
          expect(spec.max).toBeGreaterThan(0);
          expect(spec.step).toBeGreaterThan(0);
        }
      }
    }
  });

  it("keeps the counter's label set small: the attributes of every event together", () => {
    expect(attributes).toEqual(["action", "count_bucket", "filter", "has_text", "outcome", "screen", "visible_ms"]);
  });
});

describe("countBucket", () => {
  it.each([
    [0, "0"],
    [-3, "0"],
    [1, "1"],
    [2, "2-5"],
    [5, "2-5"],
    [6, "6-20"],
    [20, "6-20"],
    [21, "21+"],
    [10_000, "21+"],
  ])("%i is %s", (n, bucket) => {
    expect(countBucket(n)).toBe(bucket);
  });
});

describe("roundVisibleMs", () => {
  it.each([
    [0, 0],
    [2_499, 0],
    [2_500, 5_000],
    [7_400, 5_000],
    [7_500, 10_000],
    [VISIBLE_MS_MAX - 1, VISIBLE_MS_MAX],
    [VISIBLE_MS_MAX * 10, VISIBLE_MS_MAX],
    [-5_000, 0],
  ])("%i ms is %i", (ms, rounded) => {
    expect(roundVisibleMs(ms)).toBe(rounded);
  });
});
