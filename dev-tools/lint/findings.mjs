#!/usr/bin/env node
// A JSON tool report (stdin) as ratchet.sh findings, one line each, with no
// line numbers:
//
//   node findings.mjs oxlint   "<file> <rule>"
//   node findings.mjs knip     "<file> <issue type> <name>"
//   node findings.mjs jscpd    "<file> clone <other file>", the pair sorted,
//                              once per clone
//
// Input that is not the tool's report fails, so a run that printed an error
// instead (a bad config, a crash) can't read as a clean tree.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// A finding is space-separated, so a space inside one part would split it.
const part = (s) => String(s).split("\\").join("/").replaceAll(" ", "_");

function need(ok, what, value) {
  if (!ok) throw new Error(`${what}: ${JSON.stringify(value)}`);
}

function oxlint(report) {
  need(Array.isArray(report?.diagnostics), "not an oxlint report (no diagnostics array)", Object.keys(report ?? {}));
  return report.diagnostics.map((d) => {
    need(typeof d.filename === "string" && typeof d.code === "string" && d.code !== "", "oxlint diagnostic without a file or rule", d);
    return `${part(d.filename)} ${part(d.code)}`;
  });
}

function knipName(item) {
  if (Array.isArray(item)) return item.map(knipName).join(",");
  need(typeof item?.name === "string", "knip issue without a name", item);
  return item.name;
}

function knip(report) {
  need(Array.isArray(report?.issues), "not a knip report (no issues array)", Object.keys(report ?? {}));
  const lines = [];
  for (const issue of report.issues) {
    need(typeof issue.file === "string", "knip issue without a file", issue);
    for (const [type, items] of Object.entries(issue)) {
      if (type === "file" || !Array.isArray(items)) continue;
      for (const item of items) lines.push(`${part(issue.file)} ${type} ${part(knipName(item))}`);
    }
  }
  return lines;
}

function jscpd(report) {
  need(Array.isArray(report?.duplicates), "not a jscpd report (no duplicates array)", Object.keys(report ?? {}));
  return report.duplicates.map((d) => {
    const a = d.firstFile?.name;
    const b = d.secondFile?.name;
    need(typeof a === "string" && typeof b === "string", "jscpd clone without two files", d);
    const [x, y] = [part(a), part(b)].sort();
    return `${x} clone ${y}`;
  });
}

export const adapters = { oxlint, knip, jscpd };

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const adapter = adapters[process.argv[2]];
  if (!adapter || process.argv.length !== 3) {
    console.error(`usage: findings.mjs <${Object.keys(adapters).join("|")}> < report.json`);
    process.exit(2);
  }
  const input = readFileSync(0, "utf8");
  let report;
  try {
    report = JSON.parse(input);
  } catch {
    console.error(`${process.argv[2]} did not produce a JSON report:\n${input.slice(0, 4000)}`);
    process.exit(1);
  }
  try {
    const lines = adapter(report);
    if (lines.length > 0) process.stdout.write(lines.join("\n") + "\n");
  } catch (err) {
    console.error(err.message);
    process.exit(1);
  }
}
