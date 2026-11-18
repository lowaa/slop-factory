#!/usr/bin/env node
// Whether ROOT's node_modules is what a full `npm ci` of its package-lock.json
// makes: every package the lockfile installs on this platform, at its
// version, every workspace's included. Exits 1, naming what is missing or
// stale, when it is not.
//
//   node npm_complete.mjs ROOT
//
// oxlint's type-aware rules and knip resolve every workspace's packages; on a
// partial install (`npm ci --workspaces=false`, or a lockfile changed since
// the install) they report less rather than fail, so lint.sh asks first.
// node_modules/.package-lock.json is npm's record of the tree it installed.
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

const root = process.argv[2];
if (!root || process.argv.length !== 3) {
  console.error("usage: npm_complete.mjs ROOT");
  process.exit(2);
}

const read = (path) => JSON.parse(readFileSync(join(root, path), "utf8")).packages ?? {};
const fix = `run \`npm ci\` in ${root}: oxlint and knip need every workspace's packages`;

let installed;
try {
  installed = read("node_modules/.package-lock.json");
} catch {
  console.error(`no node_modules/.package-lock.json: ${fix}`);
  process.exit(1);
}

const problems = [];
for (const [path, entry] of Object.entries(read("package-lock.json"))) {
  // Workspace roots and links are the checkout itself; an optional package
  // (another platform's binary) may rightly be absent.
  if (!path.split("/").includes("node_modules") || entry.link || entry.optional) continue;
  const have = installed[path];
  if (!have || !existsSync(join(root, path))) problems.push(`missing ${path}`);
  else if (have.version !== entry.version) problems.push(`${path} is ${have.version}, the lockfile says ${entry.version}`);
}

if (problems.length > 0) {
  const shown = problems.slice(0, 5).join("\n  ");
  const more = problems.length > 5 ? `\n  (and ${problems.length - 5} more)` : "";
  console.error(`node_modules is not the lockfile's:\n  ${shown}${more}\n${fix}`);
  process.exit(1);
}
