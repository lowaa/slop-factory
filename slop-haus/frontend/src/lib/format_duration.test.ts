import { describe, expect, it } from "vitest";
import { formatDuration } from "./format_duration";

describe("formatDuration", () => {
  it.each([
    [0, "0s"],
    [0.4, "0s"],
    [40, "40s"],
    [60, "1m"],
    [605, "10m 5s"],
    [3600, "1h"],
    [3 * 3600 + 20 * 60 + 9, "3h 20m"],
    [86400, "1d"],
    [3 * 86400 + 4 * 3600 + 59 * 60, "3d 4h"],
    [86400 + 5, "1d"],
    [-5, "0s"],
  ])("formats %s seconds as %s", (seconds, text) => {
    expect(formatDuration(seconds)).toBe(text);
  });

  it("rounds to whole seconds before splitting into units", () => {
    expect(formatDuration(59.6)).toBe("1m");
  });
});
