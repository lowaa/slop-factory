import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { appTokenGaps, scanTokenUsage, themeTokenSets, tokensDefined, tokensMissingFrom, tokensUsed } from "./design_tokens";

describe("tokensUsed and tokensDefined", () => {
  it("reads var() calls, with or without a fallback", () => {
    const source = "a { color: var(--a); b: var( --b , 1px); c: calc(var(--c) + var(--d, var(--e))); }";
    expect([...tokensUsed(source)].sort()).toEqual(["--a", "--b", "--c", "--d", "--e"]);
  });

  it("reads definitions and not usages", () => {
    expect([...tokensDefined("a{--x:1;--y: var(--z);color:red}")].sort()).toEqual(["--x", "--y"]);
  });

  it("ignores a token named in a line or block comment", () => {
    const source = [
      "// Write z-index: var(--in-line)",
      "/* and var(--in-block),",
      "   --defined-in-block: 1; */",
      "a { z-index: var(--real); }",
    ].join("\n");
    expect([...tokensUsed(source)]).toEqual(["--real"]);
    expect([...tokensDefined(source)]).toEqual([]);
  });

  it("does not take the // of a url() for a comment", () => {
    const source = "a { background: url(https://example.com/x.png); color: var(--y); }";
    expect([...tokensUsed(source)]).toEqual(["--y"]);
  });

  it("reads a name passed to setProperty as a definition", () => {
    expect([...tokensDefined('el.style.setProperty("--shift", "1px");')]).toEqual(["--shift"]);
  });
});

describe("themeTokenSets", () => {
  const css =
    ":root{--a:1;--b:2}" +
    "@media (max-width:640px){:root{--narrow:1}}" +
    "@media (prefers-color-scheme:dark){:root{--a:3;--dark-only:1}}" +
    ':root[data-theme=dark]{--a:3;--attr-only:1}' +
    ".other{--ignored:1}";

  it("gives the default theme the plain :root block", () => {
    expect([...themeTokenSets(css).get("default")!].sort()).toEqual(["--a", "--b"]);
  });

  it("builds each dark theme on top of the default, and ignores width queries", () => {
    const themes = themeTokenSets(css);
    expect([...themes.keys()]).toEqual([
      "default",
      "@media (prefers-color-scheme:dark) :root",
      ":root[data-theme=dark]",
    ]);
    expect([...themes.get("@media (prefers-color-scheme:dark) :root")!].sort()).toEqual(["--a", "--b", "--dark-only"]);
    expect([...themes.get(":root[data-theme=dark]")!].sort()).toEqual(["--a", "--attr-only", "--b"]);
  });

  it("handles a declaration before a nested block", () => {
    expect([...themeTokenSets(":root{--a:1;@media (prefers-color-scheme:dark){--x:1}}").get("default")!]).toEqual(["--a"]);
  });

  it("rejects unbalanced braces", () => {
    expect(() => themeTokenSets(":root{--a:1")).toThrow(/unbalanced/);
    expect(() => themeTokenSets("}")).toThrow(/unbalanced/);
  });
});

describe("scanTokenUsage", () => {
  let dir: string;
  function write(path: string, source: string): void {
    mkdirSync(dirname(join(dir, path)), { recursive: true });
    writeFileSync(join(dir, path), source);
  }
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "design-tokens-"));
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it("walks nested directories and skips tests, stories and node_modules", () => {
    write("a.scss", ".a{color:var(--x)}");
    write("deep/b.tsx", 'const s = {color: "var(--y)"};');
    write("a.test.tsx", 'const s = "var(--ignored)";');
    write("a.stories.tsx", 'const s = "var(--ignored)";');
    write("node_modules/p/c.scss", ".a{color:var(--ignored)}");
    const { readers } = scanTokenUsage(dir, [".scss", ".tsx"]);
    expect([...readers.entries()].sort()).toEqual([
      ["--x", ["a.scss"]],
      ["--y", ["deep/b.tsx"]],
    ]);
  });

  it("records what each file defines for itself", () => {
    write("a.scss", ".a{--own:1;color:var(--own)}");
    expect([...scanTokenUsage(dir, [".scss"]).definedByFile.get("a.scss")!]).toEqual(["--own"]);
  });
});

describe("tokensMissingFrom", () => {
  const css = ":root{--a:1;--b:2}@media (prefers-color-scheme:dark){:root{--a:3}}";

  it("is empty when every theme defines every required token", () => {
    expect(tokensMissingFrom(css, ["--a", "--b"])).toEqual([]);
  });

  it("names a token that is absent, in each theme that lacks it", () => {
    expect(tokensMissingFrom(css, ["--a", "--c"])).toEqual([
      "--c (default)",
      "--c (@media (prefers-color-scheme:dark) :root)",
    ]);
  });

  it("counts a token a dark theme adds as missing from the default theme", () => {
    expect(tokensMissingFrom(":root{--a:1}@media (prefers-color-scheme:dark){:root{--dark:1}}", ["--dark"])).toEqual([
      "--dark (default)",
    ]);
  });
});

describe("appTokenGaps", () => {
  const usage = {
    readers: new Map([["--own", ["a.scss"]], ["--read", ["a.scss", "b.scss"]], ["--inline", ["c.scss"]]]),
    definedByFile: new Map([["a.scss", new Set(["--own"])]]),
  };
  const options = (rootCss: string) => ({
    rootCss,
    packages: [{ name: "ui", required: ["--req"] }],
    ownInternal: ["--inline"],
  });

  it("is empty when the app defines everything in every theme", () => {
    expect(appTokenGaps(options(":root{--req:1;--read:1}"), usage).size).toBe(0);
  });

  it("reports a package-required token and an app-read token the root stylesheet lacks", () => {
    const gaps = appTokenGaps(options(":root{}"), usage);
    expect([...gaps]).toEqual([
      ["--req", ["package ui"]],
      ["--read", ["app stylesheet a.scss", "app stylesheet b.scss"]],
    ]);
  });

  it("does not treat a dark theme that adds other tokens as dropping the default ones", () => {
    const css = ":root{--req:1;--read:1}:root[data-theme=dark]{--other:1}";
    expect(appTokenGaps(options(css), usage).size).toBe(0);
  });
});
