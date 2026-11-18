#!/usr/bin/env node
// Checks the frontend's module boundaries over src/ and compares the
// violations with import-baseline.json, so any debt can be paid down but never
// grown (the same ratchet as stylelint-baseline.json). The baseline is empty:
// every rule below is an error, and the baseline is only for a deliberate,
// temporary exception.
//
// Tiers: shared/ (generic widgets) and domain/ (the app's vocabulary and
// models) sit below components/, which holds one folder per feature.
//
// Rules:
//   lib-no-components  lib/, context/, api/, test/, shared/ and domain/ must not import components/
//   component-public-api  a components/<a>/ file may import another components/<b>/
//                      only through <b>/index.ts
//   use-error-message  outside lib/, `x instanceof ApiError ? x.message : ...`
//                      is spelled errorMessage(x, fallback) (lib/use_async_action)
//   no-cycle           no import cycles between files, nor between component
//                      folders (reported once per cycle group)
//
// oxlint cannot express the first two (no per-folder public-API rule), and its
// import/no-cycle has no baseline, hence this script.
//
//   node import_boundaries.mjs           check; exit 1 on drift either way
//   node import_boundaries.mjs --update  rewrite the baseline to today's violations
import { existsSync, readFileSync, readdirSync, renameSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const FRONTEND_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const BASELINE_NAME = "import-baseline.json";
const CODE_EXT = [".ts", ".tsx"];
const NO_COMPONENTS = new Set(["lib", "context", "api", "test", "shared", "domain"]);

function walk(dir, out = []) {
  for (const name of readdirSync(dir).sort()) {
    const full = path.join(dir, name);
    if (statSync(full).isDirectory()) walk(full, out);
    else if (CODE_EXT.includes(path.extname(name)) && !name.endsWith(".d.ts")) out.push(full);
  }
  return out;
}

// Resolves a specifier the way vite does for this project ("@/" and relative
// paths); anything else is a package and not our concern.
export function resolveImport(srcDir, fromFile, spec) {
  let base;
  if (spec.startsWith("@/")) base = path.join(srcDir, spec.slice(2));
  else if (spec === "." || spec.startsWith("./") || spec.startsWith("../")) base = path.resolve(path.dirname(fromFile), spec);
  else return null;
  const candidates = [base, ...CODE_EXT.map((e) => base + e), ...CODE_EXT.map((e) => path.join(base, "index" + e))];
  const hit = candidates.find((c) => existsSync(c) && statSync(c).isFile());
  return hit ?? null;
}

export function importsOf(file) {
  const info = ts.preProcessFile(readFileSync(file, "utf8"), true, true);
  return info.importedFiles.map((i) => i.fileName);
}

// "components/<name>" for files inside a component folder, else null.
function componentOf(rel) {
  const m = /^components\/([^/]+)\//.exec(rel);
  return m ? m[1] : null;
}

// Finds `x instanceof ApiError ? x.message : ...`, the idiom errorMessage()
// owns; the target is the line, so a baseline entry survives unrelated edits
// only until that line moves, which is the cue to fix it.
export function apiErrorTernaries(file) {
  const source = ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true);
  const lines = [];
  const visit = (node) => {
    if (
      ts.isConditionalExpression(node) &&
      ts.isBinaryExpression(node.condition) &&
      node.condition.operatorToken.kind === ts.SyntaxKind.InstanceOfKeyword &&
      ts.isIdentifier(node.condition.right) &&
      node.condition.right.text === "ApiError" &&
      ts.isPropertyAccessExpression(node.whenTrue) &&
      node.whenTrue.name.text === "message" &&
      node.whenTrue.expression.getText(source) === node.condition.left.getText(source)
    ) {
      lines.push(source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1);
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return lines;
}

export function violationsOf(srcDir) {
  const rel = (f) => path.relative(srcDir, f).split(path.sep).join("/");
  const graph = new Map();
  const folderGraph = new Map();
  const violations = [];
  for (const file of walk(srcDir)) {
    const from = rel(file);
    const edges = new Set();
    if (!from.startsWith("lib/") && !/\.test\.tsx?$/.test(from)) {
      for (const line of apiErrorTernaries(file)) {
        violations.push({ rule: "use-error-message", file: from, target: `line ${line}` });
      }
    }
    for (const spec of importsOf(file)) {
      const target = resolveImport(srcDir, file, spec);
      if (!target) continue;
      const to = rel(target);
      edges.add(to);
      const topDir = from.split("/")[0];
      if (NO_COMPONENTS.has(topDir) && to.startsWith("components/")) {
        violations.push({ rule: "lib-no-components", file: from, target: to });
      }
      const a = componentOf(from);
      const b = componentOf(to);
      if (a && b && a !== b) {
        if (!folderGraph.has(a)) folderGraph.set(a, new Set());
        folderGraph.get(a).add(b);
        if (!folderGraph.has(b)) folderGraph.set(b, new Set());
      }
      if (b && a !== b && !/^components\/[^/]+\/index\.tsx?$/.test(to)) {
        violations.push({ rule: "component-public-api", file: from, target: to });
      }
    }
    graph.set(from, [...edges]);
  }
  for (const cycle of cycles(graph)) {
    violations.push({ rule: "no-cycle", file: cycle[0], target: cycle.join(", ") });
  }
  const folders = new Map([...folderGraph].map(([k, v]) => [k, [...v]]));
  for (const cycle of cycles(folders)) {
    violations.push({ rule: "no-cycle", file: `components/${cycle[0]}`, target: cycle.join(" <-> ") });
  }
  return sortEntries(violations);
}

// Strongly connected components with more than one file (or a self-import),
// each as a sorted file list: one violation per cycle group, not per path.
export function cycles(graph) {
  let counter = 0;
  const index = new Map();
  const low = new Map();
  const onStack = new Set();
  const stack = [];
  const found = [];
  const visit = (v) => {
    index.set(v, counter);
    low.set(v, counter++);
    stack.push(v);
    onStack.add(v);
    for (const w of graph.get(v) ?? []) {
      if (!graph.has(w)) continue;
      if (!index.has(w)) {
        visit(w);
        low.set(v, Math.min(low.get(v), low.get(w)));
      } else if (onStack.has(w)) {
        low.set(v, Math.min(low.get(v), index.get(w)));
      }
    }
    if (low.get(v) === index.get(v)) {
      const scc = [];
      let w;
      do {
        w = stack.pop();
        onStack.delete(w);
        scc.push(w);
      } while (w !== v);
      if (scc.length > 1 || (graph.get(v) ?? []).includes(v)) found.push(scc.sort());
    }
  };
  for (const v of [...graph.keys()].sort()) if (!index.has(v)) visit(v);
  return found;
}

const keyOf = (e) => `${e.rule}\0${e.file}\0${e.target}`;
export function sortEntries(entries) {
  return [...entries].sort((a, b) => keyOf(a).localeCompare(keyOf(b)));
}

// `added`: violations not in the baseline (new debt). `stale`: baseline
// entries that no longer occur; leaving them would let the debt creep back.
export function compare(baseline, actual) {
  const allowed = new Set(baseline.map(keyOf));
  const found = new Set(actual.map(keyOf));
  return {
    added: actual.filter((e) => !allowed.has(keyOf(e))),
    stale: baseline.filter((e) => !found.has(keyOf(e))),
  };
}

function main(argv) {
  const update = argv[0] === "--update";
  const srcDir = path.resolve(FRONTEND_DIR, process.env.IMPORT_BOUNDARIES_SRC ?? "src");
  const baselineFile = path.resolve(FRONTEND_DIR, process.env.IMPORT_BOUNDARIES_BASELINE ?? BASELINE_NAME);
  const actual = violationsOf(srcDir);
  if (update) {
    // Renamed into place so an interrupted write cannot leave half a baseline.
    const tmp = `${baselineFile}.${process.pid}.tmp`;
    writeFileSync(tmp, JSON.stringify(actual, null, 2) + "\n");
    renameSync(tmp, baselineFile);
    console.log(`${baselineFile}: ${actual.length} entries`);
    return 0;
  }
  const baseline = existsSync(baselineFile) ? JSON.parse(readFileSync(baselineFile, "utf8")) : [];
  const { added, stale } = compare(baseline, actual);
  const line = (e) => `  [${e.rule}] ${e.file} -> ${e.target}`;
  if (added.length > 0) {
    console.error("New boundary violations (import through the folder's index.ts, move the code, or use errorMessage() for an ApiError message):");
    console.error(added.map(line).join("\n"));
  }
  if (stale.length > 0) {
    console.error("Baseline lists violations that are fixed -- drop them with: node tools/import_boundaries.mjs --update");
    console.error(stale.map(line).join("\n"));
  }
  return added.length + stale.length > 0 ? 1 : 0;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv.slice(2));
}
