import { describe, expect, it } from "vitest";
import {
  GUIDE_PAGES,
  classifyGuideLink,
  findGuidePage,
  guidePagesFrom,
  headingId,
  supervisorGuidePath,
} from "./guide_pages";

function headingIds(text: string): Set<string> {
  return new Set(Array.from(text.matchAll(/^#{1,4} (.+)$/gm)).map((m) => headingId(m[1])));
}

const MARKDOWN_LINK = /\[[^\]]*\]\(([^)\s]+)[^)]*\)/g;

function guideProblems(page: { slug: string; text: string }, pages = GUIDE_PAGES): string[] {
  const problems: string[] = [];
  for (const [, href] of page.text.matchAll(MARKDOWN_LINK)) {
    const link = classifyGuideLink(href, page.slug);
    if (link.kind === "invalid") problems.push(`link outside the guide: ${href}`);
    else if (link.kind === "page" && !findGuidePage(link.slug, pages)) problems.push(`link to a missing page: ${href}`);
  }
  for (const [pattern, what] of [
    [/(^|[^\w&/-])#\d+\b/, "ticket reference"],
    [/internal\/|\.go\b|\.py\b|server\//, "source path"],
  ] as const) {
    const found = pattern.exec(page.text);
    if (found) problems.push(`${what}: ${found[0].trim()}`);
  }
  return problems;
}

describe("the bundled guide", () => {
  it("has the supervisor page", () => {
    expect(findGuidePage("supervisor")?.title).toBeTruthy();
  });

  it.each(GUIDE_PAGES.map((page) => [page.slug, page] as const))("%s has no internals or links into the repo", (_, page) => {
    expect(guideProblems(page)).toEqual([]);
  });

  it("every section link names a heading of the page it points to", () => {
    for (const page of GUIDE_PAGES) {
      for (const [, href] of page.text.matchAll(MARKDOWN_LINK)) {
        const link = classifyGuideLink(href, page.slug);
        if (link.kind === "page" && link.hash) expect(headingIds(findGuidePage(link.slug)?.text ?? "").has(link.hash), href).toBe(true);
      }
    }
  });

  it("is linked to by the app only at sections that exist", () => {
    expect(headingIds(findGuidePage("supervisor")?.text ?? "").has("adding-and-managing-workers")).toBe(true);
  });
});

describe("guideProblems", () => {
  const page = (text: string) => ({ slug: "a", text });
  it.each([
    ["a repo path", "[x](../operating/supervisor.md)"],
    ["a ticket number", "see #1223 for why"],
    ["a source path", "see internal/workspace"],
    ["a go file", "in main.go"],
    ["a python file", "in envfile.py"],
    ["a server path", "under server/ here"],
    ["a missing page", "[x](nope.md)"],
  ])("catches %s", (_, text) => {
    expect(guideProblems(page(text), [{ slug: "a", title: "A", text: "" }]).length).toBeGreaterThan(0);
  });
  it("passes a clean page", () => {
    expect(guideProblems(page("[x](a.md#y) [z](https://example.com) [w](#s) text"), [{ slug: "a", title: "A", text: "" }])).toEqual([]);
  });
});

describe("classifyGuideLink", () => {
  it("routes page and section links", () => {
    expect(classifyGuideLink("other.md#s", "a")).toEqual({ kind: "page", slug: "other", hash: "s" });
    expect(classifyGuideLink("#s", "a")).toEqual({ kind: "page", slug: "a", hash: "s" });
    expect(classifyGuideLink("https://x.test", "a")).toEqual({ kind: "external" });
    expect(classifyGuideLink("../x.md", "a")).toEqual({ kind: "invalid" });
  });
});

describe("guidePagesFrom", () => {
  it("takes the slug from the file and the title from the heading", () => {
    expect(guidePagesFrom({ "/x/docs/guide/b.md": "# Bee\n", "/x/docs/guide/a.md": "no heading" })).toEqual([
      { slug: "a", title: "a", text: "no heading" },
      { slug: "b", title: "Bee", text: "# Bee\n" },
    ]);
  });
});

describe("supervisorGuidePath", () => {
  it("adds a section", () => {
    expect(supervisorGuidePath("x")).toBe("/help/supervisor#x");
    expect(supervisorGuidePath()).toBe("/help/supervisor");
  });
});
