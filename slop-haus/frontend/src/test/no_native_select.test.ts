import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";
import { SRC } from "./unstyled_story";

// The native select draws the browser's own arrow flush against the field's
// edge and an OS picker on phones; @slop-factory/ui's Dropdown is what
// slop-haus uses instead.
const NATIVE_SELECT = /<select[\s>/]|\b(?:createElement|jsxs?|jsxDEV)\(\s*["'`]select["'`]/;

// Comments mention the select they replaced; only code should trip the guard.
function withoutComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
}

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const path = join(dir, e.name);
    if (e.isDirectory()) return sourceFiles(path);
    return /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name) ? [path] : [];
  });
}

function filesWithNativeSelect(files: string[]): string[] {
  return files.filter((f) => NATIVE_SELECT.test(withoutComments(readFileSync(f, "utf8")))).map((f) => relative(SRC, f));
}

describe("native select guard", () => {
  it("matches the ways a select gets rendered", () => {
    expect(NATIVE_SELECT.test("<select value={x}>")).toBe(true);
    expect(NATIVE_SELECT.test("<select\n  value={x}")).toBe(true);
    expect(NATIVE_SELECT.test('document.createElement("select")')).toBe(true);
    expect(NATIVE_SELECT.test("<select/>")).toBe(true);
    expect(NATIVE_SELECT.test("<select />")).toBe(true);
    expect(NATIVE_SELECT.test("React.createElement('select', null)")).toBe(true);
    expect(NATIVE_SELECT.test('jsx("select", {})')).toBe(true);
    expect(NATIVE_SELECT.test("<selected>")).toBe(false);
    expect(NATIVE_SELECT.test("<Dropdown />")).toBe(false);
  });

  it("finds no component or story rendering a native <select>", () => {
    const files = sourceFiles(SRC);
    expect(files.length).toBeGreaterThan(50);
    expect(filesWithNativeSelect(files)).toEqual([]);
  });
});
