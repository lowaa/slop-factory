import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { expect, test } from "@playwright/test";

// This code serves several apps and must know about none of them: whatever is
// app-specific (URLs, paths, credentials) is passed in.
const HERE = dirname(fileURLToPath(import.meta.url));
const APP_DIRS = ["slop-haus"];

function sources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return sources(path);
    return /\.(tsx?|json)$/.test(entry.name) ? [path] : [];
  });
}

const IMPORT = /(?:from\s+|import\s*\(\s*|import\s+|require\s*\(\s*)["']([^"']+)["']/g;

// An import is the app's if it names an app or, by relative path, leaves this
// directory. The "@/" alias is each app's own tests directory.
function appImports(file: string, source: string, root: string): string[] {
  return [...source.matchAll(IMPORT)]
    .map((match) => match[1])
    .filter((specifier) => {
      if (specifier.startsWith("@/")) return true;
      if (APP_DIRS.some((app) => specifier === app || specifier.startsWith(`${app}/`) || specifier.startsWith(`@${app}/`))) {
        return true;
      }
      return specifier.startsWith(".") && relative(root, resolve(dirname(file), specifier)).startsWith("..");
    });
}

// Its own examples name apps on purpose.
const SELF = fileURLToPath(import.meta.url);

test("recognises an import of an app, by name, alias or path", () => {
  const file = join(HERE, "x.ts");
  expect(appImports(file, `import { a } from "slop-haus/e2e_playwright_ts/tests/support/env";`, HERE)).toHaveLength(1);
  expect(appImports(file, `import { a } from "../../../other-app/x";`, HERE)).toHaveLength(1);
  expect(appImports(file, `import { MAILHOG_URL } from "@/support/env";`, HERE)).toHaveLength(1);
  expect(appImports(file, `import { a } from "./mailhog";\nimport pg from "pg";`, HERE)).toEqual([]);
});

test("looks at the code it is meant to guard", () => {
  const names = sources(HERE).map((file) => relative(HERE, file));
  expect(names).toEqual(expect.arrayContaining(["mailhog.ts", "db.ts", "storage_registry.ts"]));
});

test("imports nothing from an app or from outside this directory except installed packages", () => {
  const offenders = sources(HERE)
    .filter((file) => file !== SELF)
    .flatMap((file) => appImports(file, readFileSync(file, "utf8"), HERE).map((s) => `${relative(HERE, file)}: ${s}`));
  expect(offenders).toEqual([]);
});
