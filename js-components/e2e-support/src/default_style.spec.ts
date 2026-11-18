import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { expect, test, type Page } from "@playwright/test";

import { DefaultStyleChecker, helperSource } from "./default_style";

const HELPER = `
export interface Found { description: string }
export const CURRENT = "current";
export function findInCurrentTheme(container: HTMLElement, theme: string = CURRENT): Found[] {
  return [{ description: theme }];
}
`;

test("helperSource strips the types and the exports so the file needs no module loader", () => {
  const source = helperSource(HELPER);
  expect(source).not.toMatch(/interface|: string|: Found|^export /m);
  expect(source).toMatch(/function findInCurrentTheme\(container\s*, theme\s*= CURRENT\)/);
});

test("find injects the helper and asks for the theme it was given, current by default", async () => {
  const dir = mkdtempSync(join(tmpdir(), "default-style-"));
  const path = join(dir, "check.ts");
  writeFileSync(path, HELPER);
  const evaluated: string[] = [];
  const page = {
    evaluate: async (script: string) => {
      evaluated.push(script);
      // Run it as the page would, with the globals the helper is given.
      return new Function("document", `return ${script}`)({ body: {} });
    },
  } as unknown as Page;

  const checker = new DefaultStyleChecker(path);
  expect(await checker.find(page, "dark")).toEqual([{ description: "dark" }]);
  expect(await checker.find(page)).toEqual([{ description: "current" }]);
  expect(evaluated[0]).toContain('findInCurrentTheme(document.body, "dark")');
});

test("a helper that is not there fails when the checker is made", () => {
  expect(() => new DefaultStyleChecker("/nonexistent/check.ts")).toThrow(/ENOENT/);
});
