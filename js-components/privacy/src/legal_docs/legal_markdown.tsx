import type { ComponentProps, ReactNode } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { legalTableComponents } from "./legal_table";
import "./legal_markdown.scss";

// Document bodies are written by developers, but are rendered as untrusted:
// raw HTML is dropped, not escaped into the page, and a link or image the
// renderer doesn't know to be safe is not made into one. Nothing here goes
// through dangerouslySetInnerHTML.

const SAFE_LINK = /^(https?:|mailto:|tel:)/i;

// A relative link (a path or fragment) stays on the page's own origin. A
// scheme-relative "//host" doesn't, and a "\" can be read as "/".
function isSafeHref(href: string | undefined): href is string {
  if (!href) return false;
  const h = href.trim();
  if (SAFE_LINK.test(h)) return true;
  return (h.startsWith("/") && !h.startsWith("//") && !h.includes("\\")) || h.startsWith("#");
}

function SafeLink({ href, children }: ComponentProps<"a">): ReactNode {
  if (!isSafeHref(href)) return <span>{children}</span>;
  const external = /^https?:/i.test(href.trim());
  return (
    <a href={href} {...(external ? { target: "_blank", rel: "noopener noreferrer" } : {})}>
      {children}
    </a>
  );
}

// An image is a request to somewhere the reader didn't choose to go; a
// legal text has no need of one.
function NoImage({ alt }: ComponentProps<"img">): ReactNode {
  return alt ? <span>{alt}</span> : null;
}

interface MdNode {
  type: string;
  value?: string;
  children?: MdNode[];
  data?: Record<string, unknown>;
}

const TODO = /\[\[TODO-LEGAL:([^\]]*)\]\]/g;

// Wraps [[TODO-LEGAL: ...]] markers in <mark>, so unreviewed gaps stand out.
function markTodos(node: MdNode): void {
  if (!node.children) return;
  const out: MdNode[] = [];
  for (const child of node.children) {
    if (child.type !== "text" || child.value === undefined || !child.value.includes("[[TODO-LEGAL")) {
      markTodos(child);
      out.push(child);
      continue;
    }
    let last = 0;
    for (const m of child.value.matchAll(TODO)) {
      if (m.index > last) out.push({ type: "text", value: child.value.slice(last, m.index) });
      out.push({
        type: "legalTodo",
        data: { hName: "mark", hProperties: { className: "legal-markdown__todo" } },
        children: [{ type: "text", value: `TODO: ${m[1].trim()}` }],
      });
      last = m.index + m[0].length;
    }
    if (last < child.value.length) out.push({ type: "text", value: child.value.slice(last) });
  }
  node.children = out;
}

function remarkTodoMarkers() {
  return (tree: MdNode) => markTodos(tree);
}

interface LegalMarkdownProps {
  source: string;
}

export default function LegalMarkdown({ source }: LegalMarkdownProps) {
  return (
    <div className="legal-markdown">
      <ReactMarkdown
        skipHtml
        remarkPlugins={[remarkGfm, remarkTodoMarkers]}
        components={{ a: SafeLink, img: NoImage, ...legalTableComponents }}
      >
        {source}
      </ReactMarkdown>
    </div>
  );
}
