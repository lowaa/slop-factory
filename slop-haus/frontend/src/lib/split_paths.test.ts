import { describe, expect, it } from "vitest";
import { splitPaths } from "./split_paths";

describe("splitPaths", () => {
  it("trims each line and drops blank ones", () => {
    expect(splitPaths("  a/b/ \n\n   \nc.go\n")).toEqual(["a/b/", "c.go"]);
  });

  it("gives no paths for empty text", () => {
    expect(splitPaths("")).toEqual([]);
  });
});
