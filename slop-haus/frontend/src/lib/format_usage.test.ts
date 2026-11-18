import { describe, expect, it } from "vitest";
import { formatCost, formatCount } from "./format_usage";

describe("formatCost", () => {
  it("shows dollars and cents", () => {
    expect(formatCost(12_345_000)).toBe("$12.35");
    expect(formatCost(0)).toBe("$0.00");
  });

  it("shows more places under a cent, so small usage does not read as free", () => {
    expect(formatCost(4_200)).toBe("$0.0042");
  });
});

describe("formatCount", () => {
  it("compacts large counts", () => {
    expect(formatCount(999)).toBe("999");
    expect(formatCount(1_500)).toBe("1.5k");
    expect(formatCount(2_000_000)).toBe("2M");
    expect(formatCount(123_456_789)).toBe("123M");
  });
});
