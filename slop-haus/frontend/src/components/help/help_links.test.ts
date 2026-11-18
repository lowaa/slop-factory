import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { findGuidePage } from "./guide_pages";

const SRC = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sources(path);
    return /\.tsx?$/.test(name) && !/\.(test|stories)\./.test(name) ? [path] : [];
  });
}

// Every /help/<page> the app links to must have a page. The app links with
// to="/help/$slug" and a literal slug, or with the path itself.
describe("links into the help area", () => {
  it("name pages that exist", () => {
    const named = new Set<string>();
    for (const file of sources(SRC)) {
      const text = readFileSync(file, "utf8");
      for (const m of text.matchAll(/\/help\/(?!\$)([a-z0-9_-]+)/g)) named.add(m[1]);
      for (const m of text.matchAll(/to="\/help\/\$slug"\s+params=\{\{\s*slug:\s*"([a-z0-9_-]+)"/g)) named.add(m[1]);
    }
    expect(named.size).toBeGreaterThan(0);
    for (const slug of named) expect(findGuidePage(slug), slug).toBeTruthy();
  });
});
