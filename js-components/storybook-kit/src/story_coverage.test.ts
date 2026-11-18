import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { rendersRawControl, scanStoryCoverage } from "./story_coverage";

describe("rendersRawControl", () => {
  it.each(["button", "input", "select", "textarea"])("finds a raw <%s>", (tag) => {
    expect(rendersRawControl(`export const A = () => <div><${tag} /></div>;`)).toBe(true);
    expect(rendersRawControl(`export const A = () => <${tag} className="x">hi</${tag}>;`)).toBe(true);
  });

  it("ignores comments, strings, capitalised components and other elements", () => {
    const source = `
      // <button>
      /* <input /> */
      const s = "<select>";
      const t = \`<textarea>\`;
      export const A = () => <div><Button /><Input /><span>{"<button>"}</span></div>;
    `;
    expect(rendersRawControl(source)).toBe(false);
  });
});

describe("scanStoryCoverage", () => {
  let srcDir: string;

  function write(path: string, source = ""): void {
    mkdirSync(dirname(join(srcDir, path)), { recursive: true });
    writeFileSync(join(srcDir, path), source);
  }

  const RAW = "export const A = () => <button />;";
  const PLAIN = "export const A = () => <div />;";

  beforeEach(() => {
    srcDir = mkdtempSync(join(tmpdir(), "story-coverage-"));
  });
  afterEach(() => {
    rmSync(srcDir, { recursive: true, force: true });
  });

  it("walks nested directories and ignores tests, stories and components without controls", () => {
    write("a.tsx", RAW);
    write("deep/er/b.tsx", RAW);
    write("c.tsx", PLAIN);
    write("a.test.tsx", RAW);
    write("a.stories.tsx", RAW);
    expect(scanStoryCoverage({ srcDir, coveredByParent: {} }).withControls.sort()).toEqual(["a.tsx", "deep/er/b.tsx"]);
  });

  it("reports a component with a raw control and no story, and not one with a story", () => {
    write("covered.tsx", RAW);
    write("covered.stories.tsx");
    write("bare.tsx", RAW);
    expect(scanStoryCoverage({ srcDir, coveredByParent: {} }).missing).toEqual([
      "bare.tsx (expected bare.stories.tsx)",
    ]);
  });

  it("accepts a component its parent's story covers", () => {
    write("parent.stories.tsx");
    write("child.tsx", RAW);
    const coveredByParent = { "child.tsx": { story: "parent.stories.tsx", reason: "rendered inside the parent" } };
    const coverage = scanStoryCoverage({ srcDir, coveredByParent });
    expect(coverage.missing).toEqual([]);
    expect(coverage.staleExemptions).toEqual([]);
  });

  it("flags exemptions that no longer hold", () => {
    write("parent.stories.tsx");
    write("gone.tsx", PLAIN);
    write("own.tsx", RAW);
    write("own.stories.tsx");
    write("nostory.tsx", RAW);
    write("noreason.tsx", RAW);
    const coveredByParent = {
      "gone.tsx": { story: "parent.stories.tsx", reason: "x" },
      "own.tsx": { story: "parent.stories.tsx", reason: "x" },
      "nostory.tsx": { story: "missing.stories.tsx", reason: "x" },
      "noreason.tsx": { story: "parent.stories.tsx", reason: " " },
    };
    expect(scanStoryCoverage({ srcDir, coveredByParent }).staleExemptions).toEqual([
      "gone.tsx no longer renders a raw control: drop its entry",
      "own.tsx has its own story now: drop its entry",
      "nostory.tsx maps to missing.stories.tsx, which does not exist",
      "noreason.tsx has no reason",
    ]);
  });
});
