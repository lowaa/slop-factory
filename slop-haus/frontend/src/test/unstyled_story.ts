import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import * as sass from "sass";
import ts from "typescript";
import { uiStylesImporter } from "@slop-factory/ui/styles/scss_breakpoints";

export const SRC = join(dirname(fileURLToPath(import.meta.url)), "..");

const INDEX_SCSS = join(SRC, "index.scss");
const SOURCE_EXTENSIONS = [".ts", ".tsx"];

// Source text standing in for a file on disk, so a test can replay a bug
// (a story with its stylesheet import removed) without editing the tree.
export type SourceOverrides = ReadonlyMap<string, string>;

// Every class a stylesheet defines, read from the CSS sass compiles it to
// rather than from its source: nesting (`&__x`), `@use`d modules and
// interpolation are all sass's business, and a hand-written scan of them
// would drift from what the browser really gets.
export function definedClasses(css: string): Set<string> {
  const classes = new Set<string>();
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "").replace(/"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/g, '""');
  // Only what precedes a `{` is a selector (or an at-rule prelude); the
  // declarations inside hold values like `.5s` that are not classes.
  for (const match of stripped.matchAll(/([^{};]+)\{/g)) {
    const prelude = match[1].trim();
    if (prelude.startsWith("@")) continue;
    for (const cls of prelude.matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)) classes.add(cls[1]);
  }
  return classes;
}

// The class names a className expression spells out in full. A template
// literal's text that touches a substitution (`foo--${x}`) is a fragment,
// not a class, and is skipped: the finished name is only known at runtime.
export function literalClasses(source: string): Set<string> {
  const file = ts.createSourceFile("component.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const classes = new Set<string>();
  const add = (text: string) => {
    for (const token of text.split(/\s+/)) if (token !== "") classes.add(token);
  };
  // `className={classes}` with `const classes = [...]` above it is as
  // static as an inline literal, and the blocking graph does exactly that.
  // Names are matched file-wide rather than by scope: an over-read can only
  // flag a class a sheet defines, which the exemption map can absorb.
  const initializers = new Map<string, ts.Expression[]>();
  const index = (node: ts.Node) => {
    if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.initializer) {
      initializers.set(node.name.text, [...(initializers.get(node.name.text) ?? []), node.initializer]);
    }
    ts.forEachChild(node, index);
  };
  index(file);
  const followed = new Set<string>();
  const collect = (node: ts.Node) => {
    if (ts.isIdentifier(node) && !followed.has(node.text)) {
      followed.add(node.text);
      (initializers.get(node.text) ?? []).forEach(collect);
    }
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
      add(node.text);
    } else if (ts.isTemplateExpression(node)) {
      const quasis = [node.head, ...node.templateSpans.map((span) => span.literal)];
      quasis.forEach((quasi, i) => {
        const tokens = quasi.text.split(/\s+/);
        const touchesPrevious = i > 0 && !/^\s/.test(quasi.text);
        const touchesNext = i < quasis.length - 1 && !/\s$/.test(quasi.text);
        add(tokens.slice(touchesPrevious ? 1 : 0, touchesNext ? tokens.length - 1 : tokens.length).join(" "));
      });
      node.templateSpans.forEach((span) => collect(span.expression));
      return;
    } else if (ts.isPropertyAssignment(node) && (ts.isIdentifier(node.name) || ts.isStringLiteral(node.name))) {
      // `{ "is-open": open }` in a class-name helper.
      add(node.name.text);
    }
    ts.forEachChild(node, collect);
  };
  const visit = (node: ts.Node) => {
    if (ts.isJsxAttribute(node) && node.name.getText(file) === "className" && node.initializer) {
      collect(node.initializer);
      return;
    }
    ts.forEachChild(node, visit);
  };
  visit(file);
  return classes;
}

function localImports(source: string): string[] {
  const file = ts.createSourceFile("module.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const specifiers: string[] = [];
  for (const statement of file.statements) {
    if ((ts.isImportDeclaration(statement) || ts.isExportDeclaration(statement)) && statement.moduleSpecifier && ts.isStringLiteral(statement.moduleSpecifier)) {
      specifiers.push(statement.moduleSpecifier.text);
    }
  }
  return specifiers;
}

function resolveSpecifier(from: string, specifier: string): string | null {
  let base: string;
  if (specifier.startsWith("@/")) base = join(SRC, specifier.slice(2));
  else if (specifier.startsWith(".")) base = resolve(dirname(from), specifier);
  else return null; // a package: its styles are the package's business
  const candidates = [base, ...SOURCE_EXTENSIONS.map((ext) => base + ext), ...SOURCE_EXTENSIONS.map((ext) => join(base, "index" + ext))];
  return candidates.find((path) => /\.(scss|tsx?)$/.test(path) && existsSync(path)) ?? null;
}

export function scssFiles(dir: string = SRC): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return scssFiles(path);
    return entry.name.endsWith(".scss") && !entry.name.startsWith("_") ? [path] : [];
  });
}

export function storyFiles(dir: string = SRC): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return storyFiles(path);
    return entry.name.endsWith(".stories.tsx") ? [path] : [];
  });
}

export class UnstyledStoryChecker {
  private readonly sheetClasses: Map<string, Set<string>>;

  private readonly overrides: SourceOverrides;

  constructor(overrides: SourceOverrides = new Map(), sheetClasses: Map<string, Set<string>> = new Map()) {
    this.overrides = overrides;
    this.sheetClasses = sheetClasses;
  }

  // A checker that reads the given sources instead of the files on disk but
  // shares this one's compiled stylesheets. Compiling every sheet in the repo
  // is the expensive step, and overrides only ever replace story sources, so
  // a replay need not pay for it again.
  withOverrides(overrides: SourceOverrides): UnstyledStoryChecker {
    return new UnstyledStoryChecker(overrides, this.sheetClasses);
  }

  private read(path: string): string {
    return this.overrides.get(path) ?? readFileSync(path, "utf8");
  }

  private classesOfSheet(path: string): Set<string> {
    let classes = this.sheetClasses.get(path);
    if (!classes) {
      const { css } = sass.compile(path, {
        // Mirrors the "@/" alias in vite.config.ts.
        importers: [
          { findFileUrl: (url) => (url.startsWith("@/") ? pathToFileURL(join(SRC, url.slice(2))) : null) },
          uiStylesImporter,
        ],
        logger: sass.Logger.silent,
      });
      classes = definedClasses(css);
      this.sheetClasses.set(path, classes);
    }
    return classes;
  }

  // What Storybook loads for a story: the preview's index.scss plus
  // whatever its modules import, transitively. Nothing is inherited from a
  // parent component's story or from the app shell.
  closure(story: string): { sources: string[]; sheets: string[] } {
    const sources: string[] = [];
    const sheets = new Set<string>([INDEX_SCSS]);
    const seen = new Set<string>();
    const queue = [story];
    while (queue.length > 0) {
      const path = queue.pop()!;
      if (seen.has(path)) continue;
      seen.add(path);
      if (path.endsWith(".scss")) {
        sheets.add(path);
        continue;
      }
      sources.push(path);
      for (const specifier of localImports(this.read(path))) {
        const target = resolveSpecifier(path, specifier);
        if (target) queue.push(target);
      }
    }
    return { sources, sheets: [...sheets] };
  }

  // Classes the story's modules use that some repo stylesheet defines but
  // that none of the sheets the story loads does: the story renders them
  // unstyled although the app does not.
  unstyledClasses(story: string): string[] {
    const { sources, sheets } = this.closure(story);
    const loaded = new Set(sheets.flatMap((sheet) => [...this.classesOfSheet(sheet)]));
    const definedAnywhere = new Set(scssFiles().flatMap((sheet) => [...this.classesOfSheet(sheet)]));
    const used = new Set(sources.flatMap((source) => [...literalClasses(this.read(source))]));
    return [...used].filter((cls) => definedAnywhere.has(cls) && !loaded.has(cls)).sort();
  }
}
