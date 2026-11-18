// Merges capture files into the report of style rules nothing used, per SCSS
// source file.
//
//   report.mjs --root <repo-root> --out <dir> [--include <path-prefix>]... <capture.json>...
//
// Writes <dir>/report.json and <dir>/report.md. A rule is listed when no
// capture used it, no capture reached it (see recorder.mjs), and no capture
// of the built CSS (the e2e runs, which carry no source map) used a rule
// with the same at-rule context and selector. --include limits the report
// to source files under a path prefix, e.g. the package being triaged.
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { isAbsolute, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { TraceMap, originalPositionFor } from "@jridgewell/trace-mapping";
import { CAPTURE_FORMAT } from "./lib/recorder.mjs";
import { parseRules, ruleKey } from "./lib/rules.mjs";

// Vite's dev server embeds the map base64 encoded, Sass percent-encoded.
const INLINE_MAP = /\/\*#\s*sourceMappingURL=data:application\/json(?:;charset=[^;,]+)?(;base64)?,([^\s*]+)\s*\*\//;

function inlineMap(text) {
  const m = INLINE_MAP.exec(text);
  if (!m) return null;
  const json = m[1] ? Buffer.from(m[2], "base64").toString("utf8") : decodeURIComponent(m[2]);
  return new TraceMap(JSON.parse(json));
}

// A source path as the repo knows it, or null for one that is not repo
// style source (node_modules, another tool's file).
function repoPath(source, root) {
  const abs = source.startsWith("file://") ? fileURLToPath(source) : isAbsolute(source) ? source : resolve(root, source);
  const rel = relative(root, abs);
  if (rel.startsWith("..") || isAbsolute(rel) || rel.split("/").some((p) => p === "node_modules" || p === ".storybook")) return null;
  return /\.(scss|sass|css)$/.test(rel) ? rel : null;
}

export function buildReport(captures, { root, include = [] }) {
  const usedKeys = new Set();
  // file:line:column of a rule's start in its source -> what was seen there.
  const entries = new Map();

  for (const capture of captures) {
    if (capture.format !== CAPTURE_FORMAT) throw new Error(`not a ${CAPTURE_FORMAT} file`);
    for (const sheet of capture.sheets) {
      const parsed = new Map(parseRules(sheet.text).map((r) => [r.start, r]));
      const map = inlineMap(sheet.text);
      const observed = new Map(sheet.rules.map((r) => [r.start, r]));
      for (const rule of parsed.values()) {
        const seen = observed.get(rule.start) ?? { used: false, reachable: false };
        if (seen.used) for (const s of rule.selectors) usedKeys.add(ruleKey(rule.context, s));
        if (!map) continue;
        const pos = originalPositionFor(map, { line: rule.line, column: rule.column - 1 });
        const file = pos.source ? repoPath(pos.source, root) : null;
        if (!file || (include.length && !include.some((p) => file.startsWith(p)))) continue;
        const id = `${file}:${pos.line}:${pos.column}`;
        const entry = entries.get(id) ?? { file, line: pos.line, context: rule.context, selectors: new Set(), used: false, reachable: false, keys: new Set() };
        for (const s of rule.selectors) {
          entry.selectors.add(s);
          entry.keys.add(ruleKey(rule.context, s));
        }
        entry.used ||= seen.used;
        entry.reachable ||= seen.reachable;
        entries.set(id, entry);
      }
    }
  }

  const files = {};
  for (const e of entries.values()) {
    const f = (files[e.file] ??= { rules: 0, used: 0, reachable: 0, usedByKey: 0, unused: [] });
    f.rules++;
    if (e.used) f.used++;
    else if (e.reachable) f.reachable++;
    else if ([...e.keys].some((k) => usedKeys.has(k))) f.usedByKey++;
    else f.unused.push({ line: e.line, context: e.context || undefined, selectors: [...e.selectors] });
  }
  for (const f of Object.values(files)) f.unused.sort((a, b) => a.line - b.line);
  const sorted = Object.fromEntries(Object.entries(files).sort(([a], [b]) => a.localeCompare(b)));
  const totals = { rules: 0, used: 0, reachable: 0, usedByKey: 0, unused: 0 };
  for (const f of Object.values(sorted)) {
    totals.rules += f.rules;
    totals.used += f.used;
    totals.reachable += f.reachable;
    totals.usedByKey += f.usedByKey;
    totals.unused += f.unused.length;
  }
  return { totals, files: sorted };
}

export function renderMarkdown(report) {
  const t = report.totals;
  const lines = [
    "# Unused style rules",
    "",
    `${t.rules} rules in ${Object.keys(report.files).length} source files: ${t.used} used, ${t.reachable} reachable (only by a state a story does not show), ${t.usedByKey} used through another rule or the built CSS with the same selector, **${t.unused} unused**.`,
    "",
    "| File | Rules | Used | Reachable | Same selector used | Unused |",
    "| --- | ---: | ---: | ---: | ---: | ---: |",
  ];
  for (const [file, f] of Object.entries(report.files)) {
    lines.push(`| ${file} | ${f.rules} | ${f.used} | ${f.reachable} | ${f.usedByKey} | ${f.unused.length} |`);
  }
  for (const [file, f] of Object.entries(report.files)) {
    if (f.unused.length === 0) continue;
    lines.push("", `## ${file}`, "");
    for (const u of f.unused) lines.push(`- line ${u.line}: \`${u.selectors.join(", ")}\`${u.context ? ` in \`${u.context}\`` : ""}`);
  }
  return `${lines.join("\n")}\n`;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const { values, positionals } = parseArgs({
    options: { root: { type: "string" }, out: { type: "string" }, include: { type: "string", multiple: true } },
    allowPositionals: true,
  });
  if (!values.root || !values.out || positionals.length === 0) {
    console.error("usage: report.mjs --root <repo-root> --out <dir> [--include <path-prefix>]... <capture.json>...");
    process.exit(2);
  }
  const captures = positionals.map((p) => JSON.parse(readFileSync(p, "utf8")));
  const report = buildReport(captures, { root: resolve(values.root), include: values.include ?? [] });
  mkdirSync(values.out, { recursive: true });
  writeFileSync(join(values.out, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
  writeFileSync(join(values.out, "report.md"), renderMarkdown(report));
  console.log(`css coverage: ${report.totals.unused} unused of ${report.totals.rules} rules; report in ${values.out}`);
}
