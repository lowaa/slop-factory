import { useCallback, useEffect, useRef, type ReactNode } from "react";
import { Link, useLocation } from "@slop-factory/router";
import { MarkdownView } from "@slop-factory/ui";
import { classifyGuideLink, findGuidePage, headingId, type GuidePage } from "./guide_pages";
import "./help.scss";

// One guide page. Links to other guide pages are in-app routes; a link that
// is neither those nor a web address renders as plain text, and
// guide_pages.test.ts is what keeps any from being written.
export default function GuideView({ page, pages }: { page: GuidePage; pages?: GuidePage[] }) {
  const root = useRef<HTMLDivElement>(null);
  // The router may or may not keep the fragment's leading "#".
  const hash = useLocation({ select: (location) => location.hash.replace(/^#/, "") });

  useEffect(() => {
    if (!hash) return;
    const heading = Array.from(root.current?.querySelectorAll("h1, h2, h3, h4") ?? []).find(
      (h) => headingId(h.textContent ?? "") === hash,
    );
    heading?.scrollIntoView?.();
  }, [hash, page.slug]);

  const renderLink = useCallback(
    (href: string, children: ReactNode) => {
      const link = classifyGuideLink(href, page.slug);
      if (link.kind === "external") return null;
      if (link.kind === "invalid" || !findGuidePage(link.slug, pages)) return <span>{children}</span>;
      return (
        <Link to="/help/$slug" params={{ slug: link.slug }} hash={link.hash}>
          {children}
        </Link>
      );
    },
    [page.slug, pages],
  );

  return (
    <div className="help-guide" ref={root}>
      <MarkdownView text={page.text} renderLink={renderLink} />
    </div>
  );
}
