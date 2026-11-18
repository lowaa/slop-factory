import { describe, expect, it } from "vitest";
import type { DiffLine } from "@/api";
import { collapseUnchanged } from "./diff_rows";

const eq = (text: string): DiffLine => ({ op: "equal", text });

describe("collapseUnchanged", () => {
  it("keeps changes with a little context, folding long unchanged runs", () => {
    const lines: DiffLine[] = [
      ...["a", "b", "c", "d", "e"].map(eq),
      { op: "delete", text: "old" },
      { op: "insert", text: "new" },
      ...["f", "g", "h", "i"].map(eq),
    ];

    expect(collapseUnchanged(lines, 2)).toEqual([
      { kind: "gap", hidden: 3 },
      { kind: "line", line: eq("d") },
      { kind: "line", line: eq("e") },
      { kind: "line", line: { op: "delete", text: "old" } },
      { kind: "line", line: { op: "insert", text: "new" } },
      { kind: "line", line: eq("f") },
      { kind: "line", line: eq("g") },
      { kind: "gap", hidden: 2 },
    ]);
  });

  it("does not fold a short run between two changes", () => {
    const lines: DiffLine[] = [{ op: "insert", text: "x" }, eq("a"), eq("b"), eq("c"), { op: "delete", text: "y" }];

    expect(collapseUnchanged(lines, 2).every((row) => row.kind === "line")).toBe(true);
  });
});
