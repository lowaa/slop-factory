import { describe, expect, it } from "vitest";
import { decidePlacement } from "./placement";

describe("decidePlacement", () => {
  it("opens down when the content fits below", () => {
    expect(decidePlacement({ preference: "auto", spaceBelow: 400, spaceAbove: 400, contentHeight: 120 })).toEqual({
      side: "bottom",
      maxHeight: 260,
    });
  });

  it("opens up when it does not fit below but fits above", () => {
    expect(decidePlacement({ preference: "auto", spaceBelow: 20, spaceAbove: 300, contentHeight: 120 })).toEqual({
      side: "top",
      maxHeight: 260,
    });
  });

  it("only needs room for the capped height, not a long list's full height", () => {
    expect(decidePlacement({ preference: "auto", spaceBelow: 270, spaceAbove: 900, contentHeight: 2000 }).side).toBe(
      "bottom",
    );
  });

  it("caps the height to the roomier side when neither fits", () => {
    expect(decidePlacement({ preference: "auto", spaceBelow: 80, spaceAbove: 150, contentHeight: 200 })).toEqual({
      side: "top",
      maxHeight: 150,
    });
    expect(decidePlacement({ preference: "auto", spaceBelow: 150, spaceAbove: 80, contentHeight: 200 })).toEqual({
      side: "bottom",
      maxHeight: 150,
    });
  });

  it("never returns a negative height", () => {
    expect(decidePlacement({ preference: "auto", spaceBelow: -10, spaceAbove: -30, contentHeight: 100 }).maxHeight).toBe(0);
  });

  it("lets an explicit placement override auto, still capping to its side", () => {
    expect(decidePlacement({ preference: "bottom", spaceBelow: 20, spaceAbove: 500, contentHeight: 120 })).toEqual({
      side: "bottom",
      maxHeight: 20,
    });
    expect(decidePlacement({ preference: "top", spaceBelow: 500, spaceAbove: 100, contentHeight: 120 })).toEqual({
      side: "top",
      maxHeight: 100,
    });
  });
});
