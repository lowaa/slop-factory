import { describe, expect, it } from "vitest";
import { sessionName } from "./session_name";

describe("sessionName", () => {
  it("prefers the custom title, then the first line, then Session", () => {
    expect(sessionName({ title: "Mine", first_line: "fix the bug" })).toBe("Mine");
    expect(sessionName({ title: null, first_line: "fix the bug" })).toBe("fix the bug");
    expect(sessionName({ title: null, first_line: null })).toBe("Session");
  });

  it("treats blank text as not set", () => {
    expect(sessionName({ title: "  ", first_line: "fix the bug" })).toBe("fix the bug");
    expect(sessionName({ title: "", first_line: " " })).toBe("Session");
  });
});
