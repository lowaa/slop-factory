import { describe, expect, it } from "vitest";
import { viewportShift } from "./viewport_shift";

describe("viewportShift", () => {
  it("leaves a box that already fits where it is", () => {
    expect(viewportShift(100, 300, 1000, 8)).toBe(0);
  });

  it("pushes a box past the left edge back inside the margin", () => {
    expect(viewportShift(-50, 150, 1000, 8)).toBe(58);
  });

  it("pulls a box past the right edge back inside the margin", () => {
    expect(viewportShift(900, 1100, 1000, 8)).toBe(-108);
  });

  it("treats a box exactly on the margin as fitting", () => {
    expect(viewportShift(8, 992, 1000, 8)).toBe(0);
  });

  it("pins a box wider than the viewport to the left margin", () => {
    expect(viewportShift(100, 1300, 1000, 8)).toBe(-92);
    expect(viewportShift(-100, 1300, 1000, 8)).toBe(108);
  });
});
