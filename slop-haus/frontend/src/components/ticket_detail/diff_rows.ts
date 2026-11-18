import type { DiffLine } from "@/api";

// How many unchanged lines to keep either side of a change. A description
// edit is usually a line or two in a long text; showing all of it buries
// the change, and showing none loses where it happened.
const CONTEXT_LINES = 2;

export type DiffRow = { kind: "line"; line: DiffLine } | { kind: "gap"; hidden: number };

// collapseUnchanged keeps every changed line, and unchanged ones only within
// CONTEXT_LINES of a change; each run it drops becomes one "gap" row.
export function collapseUnchanged(lines: DiffLine[], context = CONTEXT_LINES): DiffRow[] {
  const keep = lines.map(() => false);
  lines.forEach((line, i) => {
    if (line.op === "equal") return;
    for (let j = Math.max(0, i - context); j <= Math.min(lines.length - 1, i + context); j++) keep[j] = true;
  });
  const rows: DiffRow[] = [];
  let hidden = 0;
  lines.forEach((line, i) => {
    if (keep[i]) {
      if (hidden > 0) rows.push({ kind: "gap", hidden });
      hidden = 0;
      rows.push({ kind: "line", line });
    } else {
      hidden++;
    }
  });
  if (hidden > 0) rows.push({ kind: "gap", hidden });
  return rows;
}

export function countChanges(lines: DiffLine[]) {
  return {
    added: lines.filter((l) => l.op === "insert").length,
    removed: lines.filter((l) => l.op === "delete").length,
  };
}
