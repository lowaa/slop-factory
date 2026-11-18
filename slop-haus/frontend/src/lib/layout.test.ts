import { describe, expect, it } from "vitest";
import { join } from "node:path";
import { readScssBreakpoints } from "@slop-factory/ui/styles/scss_breakpoints";
import { BREAKPOINT_WIDTHS } from "./layout";

describe("breakpoints", () => {
  it("are the same widths in styles/_layout.scss and in TS", () => {
    expect(
      readScssBreakpoints(join(import.meta.dirname, "../styles/_layout.scss")),
    ).toEqual(BREAKPOINT_WIDTHS);
  });
});
