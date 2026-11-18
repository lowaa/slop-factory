import { readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { beforeAll, describe, expect, it } from "vitest";
import { SRC, UnstyledStoryChecker, definedClasses, literalClasses, storyFiles } from "./unstyled_story";

// Stories that may use a class some stylesheet defines without loading that
// stylesheet, keyed by path relative to src/. Each entry names the classes
// and why the story is right to leave them unstyled (typically: the element
// is not rendered in the story, or the class is a state hook that merely
// shares its name with a rule elsewhere). Empty today: every story loads
// the sheets for the classes it uses.
export const UNSTYLED_STORY_EXEMPT: Record<string, { classes: string[]; reason: string }> = {};

function rel(path: string): string {
  return relative(SRC, path);
}

const checker = new UnstyledStoryChecker();

describe("definedClasses", () => {
  it("reads classes from selectors, nested or not, and ignores values and at-rule preludes", () => {
    const css = `
      /* .commented { } */
      .a__b, .a:hover .c-d { transition: opacity .5s; }
      @media (min-width: 40em) { .e[href$=".pdf"] > .f_g { margin: 0 } }
      @keyframes spin { from { opacity: 0 } }
    `;
    expect([...definedClasses(css)].sort()).toEqual(["a", "a__b", "c-d", "e", "f_g"]);
  });
});

describe("literalClasses", () => {
  it("collects literal class names and skips template fragments", () => {
    const source = `
      const A = () => <div className="one two" />;
      const B = () => <div className={on ? "three" : "four"} />;
      const C = () => <div className={\`five six--\${size} \${tone}-seven eight\`} />;
      const D = () => <div className={cx({ "nine": on })} />;
      const F = () => {
        const classes = ["eleven", ok ? "twelve" : "thirteen"].join(" ");
        return <div className={classes} />;
      };
      const E = () => <div id="not-a-class" />;
    `;
    expect([...literalClasses(source)].sort()).toEqual(["eight", "eleven", "five", "four", "nine", "one", "thirteen", "three", "twelve", "two"]);
  });
});

describe("unstyled stories", () => {
  const stories = storyFiles();

  it("finds the stories to check", () => {
    // Guards against the walk silently matching nothing.
    expect(stories.length).toBeGreaterThan(40);
  });

  it("loads, for every story, a stylesheet defining each class it renders", () => {
    const offenders = stories
      .map((story) => ({ story: rel(story), classes: checker.unstyledClasses(story).filter((cls) => !UNSTYLED_STORY_EXEMPT[rel(story)]?.classes.includes(cls)) }))
      .filter(({ classes }) => classes.length > 0)
      .map(({ story, classes }) => `${story}: ${classes.join(", ")}`);
    expect(
      offenders,
      `stories whose modules use classes no stylesheet they load defines (import the sheet in the story, or exempt it with a reason):\n${offenders.join("\n")}`,
    ).toEqual([]);
    // Parses every story and stylesheet: about 2s here but 5s in the
    // containerised run, and it timed out at 30s on a loaded CI runner.
  }, 90_000);

  it("keeps UNSTYLED_STORY_EXEMPT honest", () => {
    for (const [story, { classes, reason }] of Object.entries(UNSTYLED_STORY_EXEMPT)) {
      const flagged = checker.unstyledClasses(join(SRC, story));
      for (const cls of classes) expect(flagged, `${story} no longer leaves .${cls} unstyled: drop it from its entry`).toContain(cls);
      expect(reason).not.toBe("");
    }
  });
});

// Replays of the two incidents: the fix was one stylesheet import in the
// story, so the story minus that line must be flagged.
describe("past incidents", () => {
  // Compiling every stylesheet is the slow step and the replays share it
  // through the checker, so pay for it once here rather than inside whichever
  // case runs first, where it would race the 5s default timeout on a loaded
  // CI agent.
  beforeAll(() => {
    checker.unstyledClasses(join(SRC, "components/reports_panel/llm_usage_tickets.stories.tsx"));
  }, 30_000);

  function withoutImport(story: string, line: string): UnstyledStoryChecker {
    const path = join(SRC, story);
    const source = readFileSync(path, "utf8");
    expect(source, `${story} no longer has ${line}: update this replay`).toContain(line);
    return checker.withOverrides(new Map([[path, source.replace(line, "")]]));
  }

  it("llm_usage_tickets story without its llm_usage.scss import", () => {
    const story = "components/reports_panel/llm_usage_tickets.stories.tsx";
    expect(checker.unstyledClasses(join(SRC, story))).toEqual([]);
    expect(withoutImport(story, 'import "./llm_usage.scss";').unstyledClasses(join(SRC, story))).toContain("llm-usage__note");
  });

  it("paths_editor story without ticket_detail.scss, whose own sheet re-opens the same block", () => {
    const story = "components/ticket_detail/paths_editor.stories.tsx";
    expect(checker.unstyledClasses(join(SRC, story))).toEqual([]);
    const flagged = withoutImport(story, 'import "./ticket_detail.scss";').unstyledClasses(join(SRC, story));
    expect(flagged).toContain("ticket-detail__notice");
    expect(flagged).toContain("ticket-detail__hint");
  });
});
