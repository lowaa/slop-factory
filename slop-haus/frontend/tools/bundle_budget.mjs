#!/usr/bin/env node
// Fails when the JavaScript every visitor downloads before the first screen
// (the login page's included) outgrows its budget. That is the entry chunk
// plus the chunks it imports statically, read from the build's manifest:
// the entry file alone would not do, as a page imported eagerly can land in
// a shared chunk the entry merely imports.
//
// Route pages are lazy (see routes.tsx), so what grows this number is an
// eager import of a page or its dependencies: reports, Remote Control and
// the like should stay in chunks of their own.
//
//   node bundle_budget.mjs <dist dir>     (built with `vite build --manifest`)
//
// Raise the budget only when the initial load has to grow, and say why in
// the change that does it.
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

// Gzip bytes. The initial load is ~177 kB: the pages are split off, and the
// markdown stack stays out because js-components' packages declare
// sideEffects (a barrel import of Button must not keep MarkdownView alive).
// ~8 kB of headroom, so a dependency that lands in the entry chunk by
// accident fails here rather than going unnoticed.
export const INITIAL_GZIP_BUDGET = 185_000;

// initialChunks returns the manifest entries loaded up front: the entry
// (isEntry) and everything reachable through `imports`, never through
// `dynamicImports`.
export function initialChunks(manifest) {
  const entries = Object.entries(manifest).filter(([, chunk]) => chunk.isEntry);
  if (entries.length !== 1) throw new Error(`expected one entry chunk in the manifest, found ${entries.length}`);
  const seen = new Set();
  const visit = (key) => {
    if (seen.has(key)) return;
    seen.add(key);
    for (const dep of manifest[key].imports ?? []) visit(dep);
  };
  visit(entries[0][0]);
  return [...seen].map((key) => manifest[key]);
}

export function checkBudget(distDir) {
  const manifest = JSON.parse(readFileSync(path.join(distDir, ".vite", "manifest.json"), "utf8"));
  const chunks = initialChunks(manifest);
  const bytes = chunks.reduce((sum, chunk) => sum + gzipSync(readFileSync(path.join(distDir, chunk.file))).length, 0);
  return { files: chunks.map((chunk) => chunk.file), bytes, ok: bytes <= INITIAL_GZIP_BUDGET };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const dist = process.argv[2];
  if (!dist) {
    console.error("usage: bundle_budget.mjs <dist dir>");
    process.exit(2);
  }
  const { files, bytes, ok } = checkBudget(dist);
  const line = `initial JS ${bytes} bytes gzip in ${files.length} chunks (budget ${INITIAL_GZIP_BUDGET})`;
  if (!ok) {
    console.error(`FAIL: ${line}\n${files.join("\n")}\nA page or its dependencies is probably imported eagerly: load it with lazy().`);
    process.exit(1);
  }
  console.log(`ok: ${line}`);
}
