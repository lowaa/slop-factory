import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { compile } from "sass";
import { describe, expect, it } from "vitest";

// Shared components read design tokens as var(--token), with no fallback. A
// token a host never defines leaves the property unset, so these checks make
// the dependency explicit: a package lists the tokens it reads, and an app's
// test (and the shared Storybook preview) fails if it doesn't define them.

const TOKEN_USAGE = /var\(\s*(--[a-zA-Z0-9_-]+)/g;
// A declaration, or the quoted name passed to style.setProperty.
const TOKEN_DEFINITION = /(?:(?:^|[;{\s])(--[a-zA-Z0-9_-]+)\s*:|["'`](--[a-zA-Z0-9_-]+)["'`])/g;

// A token named in a comment is documentation, not a read or a definition.
// A `//` right after a `:` is a URL scheme (url(https://...)), not a comment.
const COMMENTS = /\/\*[\s\S]*?\*\/|(?<!:)\/\/[^\n]*/g;

function stripComments(source: string): string {
  return source.replace(COMMENTS, "");
}

export function tokensUsed(source: string): Set<string> {
  return new Set([...stripComments(source).matchAll(TOKEN_USAGE)].map((match) => match[1]));
}

export function tokensDefined(source: string): Set<string> {
  return new Set([...stripComments(source).matchAll(TOKEN_DEFINITION)].map((match) => match[1] ?? match[2]));
}

// Style sources: .scss, plus .ts/.tsx for a var() written in an inline style.
function styleSources(dir: string, extensions: readonly string[]): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    if (entry.name === "node_modules") return [];
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return styleSources(path, extensions);
    const isTest = /\.(test|stories)\.tsx?$/.test(entry.name);
    return !isTest && extensions.some((ext) => entry.name.endsWith(ext)) ? [path] : [];
  });
}

export interface TokenUsage {
  // Every token read, with the files (relative to srcDir) reading it.
  readers: Map<string, string[]>;
  // Per file, the tokens it defines itself.
  definedByFile: Map<string, Set<string>>;
}

export function scanTokenUsage(srcDir: string, extensions: readonly string[]): TokenUsage {
  const readers = new Map<string, string[]>();
  const definedByFile = new Map<string, Set<string>>();
  for (const path of styleSources(srcDir, extensions)) {
    const file = relative(srcDir, path);
    const source = readFileSync(path, "utf8");
    definedByFile.set(file, tokensDefined(source));
    for (const token of tokensUsed(source)) {
      readers.set(token, [...(readers.get(token) ?? []), file]);
    }
  }
  return { readers, definedByFile };
}

export interface PackageTokensOptions {
  srcDir: string;
  // Tokens the app must define for this package to look right.
  required: readonly string[];
  // Tokens the package sets for itself (e.g. an inline style), not asked of
  // the app.
  internal: readonly string[];
  // The fewest distinct tokens expected to be read. Guards against the walk
  // silently matching nothing.
  mustFind: number;
}

const PREVIEW_TOKENS = join(dirname(fileURLToPath(import.meta.url)), "..", ".storybook", "preview_tokens.scss");

// The required tokens a compiled stylesheet does not define, per theme.
export function tokensMissingFrom(css: string, required: readonly string[]): string[] {
  return [...themeTokenSets(css)].flatMap(([theme, defined]) =>
    required.filter((token) => !defined.has(token)).map((token) => `${token} (${theme})`),
  );
}

// Registers a package's token tests. Call it from a thin `tokens.test.ts`.
export function checkPackageTokens(options: PackageTokensOptions): void {
  const { srcDir, required, internal } = options;
  const listed = new Set([...required, ...internal]);
  const usage = scanTokenUsage(srcDir, [".scss", ".ts", ".tsx"]);

  describe("design tokens", () => {
    it("finds the tokens the package reads", () => {
      expect(usage.readers.size).toBeGreaterThanOrEqual(options.mustFind);
    });

    it("lists every token the package reads, as required or internal", () => {
      const unlisted = [...usage.readers]
        .filter(([token]) => !listed.has(token))
        .map(([token, files]) => `${token} (read in ${files.join(", ")})`);
      expect(unlisted, "add to REQUIRED_TOKENS, or INTERNAL_TOKENS if the package sets it itself").toEqual([]);
    });

    it("is supplied by the shared Storybook preview tokens, in every theme", () => {
      const missing = tokensMissingFrom(compile(PREVIEW_TOKENS, { style: "compressed" }).css, required);
      expect(missing, "define it in storybook-kit/.storybook/preview_tokens.scss").toEqual([]);
    });

    it("lists no token the package no longer reads", () => {
      expect([...listed].filter((token) => !usage.readers.has(token))).toEqual([]);
    });

    it("only lists as internal a token the package sets itself", () => {
      const defined = new Set([...usage.definedByFile.values()].flatMap((tokens) => [...tokens]));
      expect(internal.filter((token) => !defined.has(token))).toEqual([]);
    });

    it("does not list a token as both required and internal", () => {
      expect(required.filter((token) => internal.includes(token))).toEqual([]);
    });

    it("keeps the required list sorted and free of duplicates", () => {
      expect(required).toEqual([...new Set(required)].sort());
    });
  });
}

interface Frame {
  prelude: string;
  own: string;
}

// The tokens each theme of a compiled stylesheet defines on :root. The
// default theme is the plain :root block. Any other :root block under a
// prefers-color-scheme media query or a [data-theme] selector is a theme of
// its own, and defines on top of the default, so its set includes the
// default's. Other conditional :root blocks (a width query) are not themes.
//
// Reads compiled CSS, not SCSS, so mixins and includes are already expanded.
// Expects sass's "compressed" style, which has no comments.
export function themeTokenSets(css: string): Map<string, Set<string>> {
  const base = new Set<string>();
  const variants = new Map<string, Set<string>>();
  const stack: Frame[] = [{ prelude: "", own: "" }];

  const record = (frame: Frame, ancestors: Frame[]) => {
    const tokens = tokensDefined(frame.own);
    const conditions = ancestors.map((a) => a.prelude).filter((p) => p !== "");
    for (const selector of frame.prelude.split(",").map((s) => s.trim())) {
      if (!selector.startsWith(":root")) continue;
      const isTheme = selector.includes("[data-theme") || conditions.some((c) => c.includes("prefers-color-scheme"));
      if (conditions.length === 0 && selector === ":root") {
        tokens.forEach((t) => base.add(t));
      } else if (isTheme) {
        const name = [...conditions, selector].join(" ");
        const set = variants.get(name) ?? new Set<string>();
        tokens.forEach((t) => set.add(t));
        variants.set(name, set);
      }
    }
  };

  for (const char of css) {
    const top = stack[stack.length - 1];
    if (char === "{") {
      const split = top.own.lastIndexOf(";");
      stack.push({ prelude: top.own.slice(split + 1).trim(), own: "" });
      top.own = top.own.slice(0, split + 1);
    } else if (char === "}") {
      const frame = stack.pop();
      if (frame === undefined || stack.length === 0) throw new Error("unbalanced braces in stylesheet");
      record(frame, stack.slice(1));
    } else {
      top.own += char;
    }
  }
  if (stack.length !== 1) throw new Error("unbalanced braces in stylesheet");

  const themes = new Map<string, Set<string>>([["default", base]]);
  for (const [name, set] of variants) themes.set(name, new Set([...base, ...set]));
  return themes;
}

export interface PackageRequirement {
  name: string;
  required: readonly string[];
}

export interface AppTokensOptions {
  // The app's src dir: its own stylesheets are scanned for tokens it reads.
  srcDir: string;
  // The app's root stylesheet compiled with sass's "compressed" style.
  rootCss: string;
  // The js-components packages the app depends on.
  packages: readonly PackageRequirement[];
  // Tokens the app's own stylesheets read but get from elsewhere (set inline
  // by a component, say), not from the root stylesheet.
  ownInternal?: readonly string[];
  // The fewest themes expected. Guards against the parse matching nothing.
  mustFindThemes?: number;
}

// A token every stylesheet reads would otherwise print dozens of paths.
function summarise(readers: readonly string[]): string {
  const shown = readers.slice(0, 4).join(", ");
  return readers.length > 4 ? `${shown} and ${readers.length - 4} more` : shown;
}

// token -> who needs it, for each token the app does not define in every theme.
export function appTokenGaps(
  options: Pick<AppTokensOptions, "rootCss" | "packages" | "ownInternal">,
  usage: TokenUsage,
): Map<string, string[]> {
  const themes = themeTokenSets(options.rootCss);
  const definedEverywhere = (token: string) => [...themes.values()].every((defined) => defined.has(token));
  const gaps = new Map<string, string[]>();
  const addGap = (token: string, reader: string) => gaps.set(token, [...(gaps.get(token) ?? []), reader]);

  for (const { name, required } of options.packages) {
    for (const token of required) {
      if (!definedEverywhere(token)) addGap(token, `package ${name}`);
    }
  }

  const ownInternal = new Set(options.ownInternal ?? []);
  for (const [token, files] of usage.readers) {
    if (ownInternal.has(token) || definedEverywhere(token)) continue;
    // A stylesheet may define and read its own scoped property.
    for (const file of files.filter((f) => !usage.definedByFile.get(f)?.has(token))) {
      addGap(token, `app stylesheet ${file}`);
    }
  }
  return gaps;
}

// Registers an app's token tests. Call it from a thin `design_tokens.test.ts`.
export function checkAppTokens(options: AppTokensOptions): void {
  const themes = themeTokenSets(options.rootCss);
  const usage = scanTokenUsage(options.srcDir, [".scss"]);
  const gaps = appTokenGaps(options, usage);

  describe("design tokens", () => {
    it("finds the themes the app defines", () => {
      expect(themes.get("default")?.size ?? 0).toBeGreaterThan(0);
      expect(themes.size).toBeGreaterThanOrEqual(options.mustFindThemes ?? 1);
    });

    it("finds the stylesheets to check", () => {
      expect(usage.readers.size).toBeGreaterThan(0);
    });

    it("has no undefined token", () => {
      const newGaps = [...gaps]
        .map(([token, readers]) => `${token} (needed by ${summarise(readers)})`);
      expect(newGaps, "define the token in every theme of the root stylesheet").toEqual([]);
    });
  });
}
