import { describe, expect, it } from "vitest";
import { mockOffsetSize } from "@/test/layout_mock";

// The second test only means something while it runs after the first, so
// they share a describe and rely on in-file order.
describe("mockOffsetSize", () => {
  it("sizes elements for the test that asks", () => {
    mockOffsetSize({ height: 600, width: (el) => (el.id === "wide" ? 900 : 10) });
    const el = document.createElement("div");
    el.id = "wide";
    expect(el.offsetHeight).toBe(600);
    expect(el.offsetWidth).toBe(900);
    expect(el.clientHeight).toBe(600);
    expect(el.clientWidth).toBe(900);
  });

  it("is gone again in the next test", () => {
    const el = document.createElement("div");
    expect(el.offsetHeight).toBe(0);
    expect(el.offsetWidth).toBe(0);
    expect(el.clientHeight).toBe(0);
    expect(el.clientWidth).toBe(0);
  });
});
