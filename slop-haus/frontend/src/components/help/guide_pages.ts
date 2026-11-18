// The user guide, bundled at build time from docs/guide/*.md. The files stay
// in docs/ so they read on their own too; only how a link between them
// becomes an in-app route is decided here.
const RAW_PAGES = import.meta.glob<string>("../../../../docs/guide/*.md", {
  query: "?raw",
  import: "default",
  eager: true,
});

export interface GuidePage {
  slug: string;
  title: string;
  text: string;
}

function slugOf(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1).replace(/\.md$/, "");
}

function titleOf(slug: string, text: string): string {
  return /^# (.+)$/m.exec(text)?.[1].trim() ?? slug;
}

export function guidePagesFrom(raw: Record<string, string>): GuidePage[] {
  return Object.entries(raw)
    .map(([path, text]) => ({ slug: slugOf(path), title: titleOf(slugOf(path), text), text }))
    .sort((a, b) => a.slug.localeCompare(b.slug));
}

export const GUIDE_PAGES = guidePagesFrom(RAW_PAGES);

export function findGuidePage(slug: string, pages: GuidePage[] = GUIDE_PAGES): GuidePage | undefined {
  return pages.find((page) => page.slug === slug);
}

export type GuideLink =
  | { kind: "external" }
  | { kind: "page"; slug: string; hash: string | undefined }
  | { kind: "invalid" };

// How a link in a guide page is followed: an absolute web link as it is, a
// link to another guide page (`other.md#section`) or to a section of this one
// (`#section`) as an in-app route, and anything else relative as invalid,
// since a path into the repo goes nowhere in the app.
export function classifyGuideLink(href: string, currentSlug: string): GuideLink {
  if (/^(https?:|mailto:)/i.test(href)) return { kind: "external" };
  if (href.startsWith("#")) return { kind: "page", slug: currentSlug, hash: href.slice(1) || undefined };
  const match = /^([a-z0-9_-]+)\.md(?:#([\w-]+))?$/.exec(href);
  if (!match) return { kind: "invalid" };
  return { kind: "page", slug: match[1], hash: match[2] };
}

// GitHub's heading ids, which is what a `#section` link in the markdown means
// when the file is read there.
export function headingId(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s-]/gu, "")
    .trim()
    .replace(/\s/g, "-");
}

export const HELP_PATH = "/help";
export const supervisorGuidePath = (hash?: string) => `${HELP_PATH}/supervisor${hash ? `#${hash}` : ""}`;
