import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import ts from "typescript";
import { describe, expect, it } from "vitest";

// The raw elements the default-look check (default_style_check.ts) polices
// in stories. A component rendering one with no story is never checked.
const CONTROL_TAGS: ReadonlySet<string> = new Set(["button", "input", "select", "textarea"]);

// A component with no story of its own because another story renders its
// controls: which story, and why that is enough (typically: it renders the
// component inside the parent's states and the stylesheet is shared).
export interface CoveredByParent {
  story: string;
  reason: string;
}

export interface StoryCoverageOptions {
  // The source tree to scan; exemption keys and `story` are relative to it.
  srcDir: string;
  coveredByParent: Record<string, CoveredByParent>;
  // The fewest components expected to render a raw control. Guards against
  // the walk or the parse silently matching nothing.
  mustFind: number;
}

// Matches components, not tests or stories.
function isComponentFile(name: string): boolean {
  return name.endsWith(".tsx") && !/\.(test|stories)\.tsx$/.test(name);
}

function tsxFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return tsxFiles(path);
    return isComponentFile(entry.name) ? [path] : [];
  });
}

// Parses with the TypeScript compiler rather than a regex: only JSX elements
// count, so `<button>` in a comment, a string or a generic type argument is
// not a control, and a `<Button>` component (capitalised) is not a raw one.
export function rendersRawControl(source: string): boolean {
  const file = ts.createSourceFile("component.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  let found = false;
  const visit = (node: ts.Node) => {
    if (found) return;
    if (
      (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) &&
      CONTROL_TAGS.has(node.tagName.getText(file))
    ) {
      found = true;
      return;
    }
    ts.forEachChild(node, visit);
  };
  visit(file);
  return found;
}

function storyFor(path: string): string {
  return path.replace(/\.tsx$/, ".stories.tsx");
}

export interface StoryCoverage {
  // Components that render a raw control, relative to srcDir.
  withControls: string[];
  // Of those, the ones with no story next to them and no exemption.
  missing: string[];
  // Exemptions that no longer hold, each saying why.
  staleExemptions: string[];
}

export function scanStoryCoverage(options: Pick<StoryCoverageOptions, "srcDir" | "coveredByParent">): StoryCoverage {
  const { srcDir, coveredByParent } = options;
  const rel = (path: string) => relative(srcDir, path);
  const withControls = tsxFiles(srcDir).filter((path) => rendersRawControl(readFileSync(path, "utf8")));
  const withControlsRel = new Set(withControls.map(rel));

  const missing = withControls
    .filter((path) => !existsSync(storyFor(path)) && !(rel(path) in coveredByParent))
    .map((path) => `${rel(path)} (expected ${rel(storyFor(path))})`);

  const staleExemptions: string[] = [];
  for (const [file, { story, reason }] of Object.entries(coveredByParent)) {
    if (!withControlsRel.has(file)) {
      staleExemptions.push(`${file} no longer renders a raw control: drop its entry`);
    } else if (existsSync(storyFor(join(srcDir, file)))) {
      staleExemptions.push(`${file} has its own story now: drop its entry`);
    }
    if (!existsSync(join(srcDir, story))) {
      staleExemptions.push(`${file} maps to ${story}, which does not exist`);
    }
    if (reason.trim() === "") {
      staleExemptions.push(`${file} has no reason`);
    }
  }
  return { withControls: withControls.map(rel), missing, staleExemptions };
}

// Registers the app's story coverage tests. Call it from a thin
// `story_coverage.test.ts` that holds the app's own exemption map.
export function checkStoryCoverage(options: StoryCoverageOptions): void {
  describe("story coverage", () => {
    const coverage = scanStoryCoverage(options);

    it("finds the components to check", () => {
      expect(coverage.withControls.length).toBeGreaterThanOrEqual(options.mustFind);
    });

    it("gives every component that renders a raw form control a story next to it", () => {
      expect(coverage.missing, `components with raw controls and no story:\n${coverage.missing.join("\n")}`).toEqual([]);
    });

    it("keeps the exemptions honest", () => {
      expect(coverage.staleExemptions).toEqual([]);
    });
  });
}
