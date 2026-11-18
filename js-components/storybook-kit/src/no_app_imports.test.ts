import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// The kit serves several apps and must know about none of them: whatever is
// app-specific (providers, exemption maps, theme switches) is passed in.
const KIT = join(dirname(fileURLToPath(import.meta.url)), "..");
const APP_DIRS = ["slop-haus"];

function sources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    if (entry.name === "node_modules" || entry.name === "storybook-static") return [];
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return sources(path);
    return /\.(tsx?|json|scss)$/.test(entry.name) && !path.endsWith("package-lock.json") ? [path] : [];
  });
}

// Its own examples name apps on purpose.
const SELF = fileURLToPath(import.meta.url);

const IMPORT = /(?:from\s+|import\s*\(\s*|import\s+|require\s*\(\s*)["']([^"']+)["']/g;

function appImports(file: string, source: string): string[] {
  return [...source.matchAll(IMPORT)]
    .map((match) => match[1])
    .filter((specifier) => {
      if (APP_DIRS.some((app) => specifier === app || specifier.startsWith(`${app}/`) || specifier.startsWith(`@${app}/`))) {
        return true;
      }
      if (!specifier.startsWith(".")) return false;
      const target = relative(KIT, resolve(dirname(file), specifier));
      return target.startsWith("..");
    });
}

describe("storybook-kit has no app imports", () => {
  it("recognises an import of an app, by name or by path", () => {
    const file = join(KIT, "src", "x.ts");
    expect(appImports(file, `import { a } from "slop-haus/frontend/src/api";`)).toHaveLength(1);
    expect(appImports(file, `import { a } from "../../../other-app/frontend/src/api";`)).toHaveLength(1);
    expect(appImports(file, `const m = await import("@/api");\nimport x from "./api_guard";`)).toEqual([]);
  });

  it("imports nothing from outside the package except installed packages", () => {
    const offenders = sources(KIT)
      .filter((file) => file !== SELF)
      .flatMap((file) =>
      appImports(file, readFileSync(file, "utf8")).map((specifier) => `${relative(KIT, file)}: ${specifier}`),
    );
    expect(offenders).toEqual([]);
  });
});
