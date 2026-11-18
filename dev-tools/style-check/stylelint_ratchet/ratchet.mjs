#!/usr/bin/env node
// Runs the repo's stylelint config over one area and compares the violations
// with that area's stylelint-baseline.json, so existing debt can be paid down
// but never grown.
//
//   node ratchet.mjs <area-dir>           check; exit 1 on drift either way
//   node ratchet.mjs --update <area-dir>  rewrite the baseline to the real counts
import { existsSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import stylelint from "stylelint";

const BASELINE_NAME = "stylelint-baseline.json";
const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
// The config's file patterns are relative to its own directory; tests point
// this at a scratch tree holding a copy of the config and laid out like the repo.
const LINT_ROOT = process.env.STYLELINT_RATCHET_ROOT ?? REPO_ROOT;

const key = (entry) => `${entry.file}\0${entry.rule}`;

// Violations as sorted {file, rule, count} entries, files relative to areaDir.
export function tally(results, areaDir) {
  const counts = new Map();
  for (const result of results) {
    const file = path.relative(areaDir, result.source).split(path.sep).join("/");
    for (const warning of result.warnings) {
      const entry = counts.get(`${file}\0${warning.rule}`) ?? { file, rule: warning.rule, count: 0 };
      entry.count += 1;
      counts.set(`${file}\0${warning.rule}`, entry);
    }
  }
  return sortEntries([...counts.values()]);
}

export function sortEntries(entries) {
  return [...entries].sort((a, b) => (a.file + "\0" + a.rule).localeCompare(b.file + "\0" + b.rule));
}

// `over`: more violations than the baseline allows (new debt).
// `stale`: the baseline allows more than there are, so it was not lowered when
// debt was paid; leaving the slack would let the same debt creep back in.
export function compare(baseline, actual) {
  const allowed = new Map(baseline.map((e) => [key(e), e.count]));
  const found = new Map(actual.map((e) => [key(e), e.count]));
  const over = [];
  const stale = [];
  for (const e of actual) {
    const limit = allowed.get(key(e)) ?? 0;
    if (e.count > limit) over.push({ file: e.file, rule: e.rule, baseline: limit, actual: e.count });
  }
  for (const e of baseline) {
    const count = found.get(key(e)) ?? 0;
    if (count < e.count) stale.push({ file: e.file, rule: e.rule, baseline: e.count, actual: count });
  }
  return { over, stale };
}

function readBaseline(file) {
  if (!existsSync(file)) return [];
  const entries = JSON.parse(readFileSync(file, "utf8"));
  if (!Array.isArray(entries)) throw new Error(`${file}: expected an array of {file, rule, count}`);
  return entries;
}

async function main(argv) {
  const update = argv[0] === "--update";
  const areaArg = update ? argv[1] : argv[0];
  if (!areaArg || argv.length !== (update ? 2 : 1)) {
    console.error("usage: ratchet.mjs [--update] <area-dir>");
    return 2;
  }
  const areaDir = path.resolve(areaArg);
  const baselineFile = path.join(areaDir, BASELINE_NAME);

  const { results } = await stylelint.lint({
    files: [`${path.relative(LINT_ROOT, areaDir)}/**/*.scss`],
    cwd: LINT_ROOT,
    configFile: path.join(LINT_ROOT, "stylelint.config.mjs"),
    allowEmptyInput: true,
  });

  // A file stylelint cannot parse is never counted as debt: nothing else
  // about it would have been checked.
  const broken = results.flatMap((r) =>
    r.warnings.filter((w) => w.rule === "CssSyntaxError").map((w) => `${r.source}: ${w.text}`),
  );
  if (broken.length > 0) {
    console.error(`stylelint could not parse:\n${broken.join("\n")}`);
    return 1;
  }

  const actual = tally(results, areaDir);
  if (update) {
    // Renamed into place so an interrupted write cannot leave half a baseline.
    const tmp = `${baselineFile}.${process.pid}.tmp`;
    writeFileSync(tmp, JSON.stringify(actual, null, 2) + "\n");
    renameSync(tmp, baselineFile);
    console.log(`${baselineFile}: ${actual.length} entries`);
    return 0;
  }

  const { over, stale } = compare(readBaseline(baselineFile), actual);
  const line = (d) => `  ${d.file}  ${d.rule}  baseline ${d.baseline}, now ${d.actual}`;
  if (over.length > 0) {
    console.error(`New style violations (fix them, or add a stylelint-disable-next-line with a reason):`);
    console.error(over.map(line).join("\n"));
  }
  if (stale.length > 0) {
    console.error(`Baseline is higher than the real count -- lower it with: node ${path.relative(process.cwd(), fileURLToPath(import.meta.url))} --update ${areaArg}`);
    console.error(stale.map(line).join("\n"));
  }
  return over.length + stale.length > 0 ? 1 : 0;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  process.exitCode = await main(process.argv.slice(2));
}
