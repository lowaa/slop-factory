import { createContext, memo, useContext, useState, type ComponentProps, type ReactNode } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import Button from "../button/button";
import "./markdown_view.scss";

// The text is untrusted (ticket bodies, agent messages). Safety rests on
// react-markdown building React elements: raw HTML is never parsed (no
// rehype-raw, and skipHtml drops it rather than showing it), and nothing here
// uses dangerouslySetInnerHTML. Do not add a raw-HTML plugin.

const SAFE_URL = /^(https?:|mailto:)/i;

function safeUrl(url: string | undefined): string | null {
  const u = url?.trim();
  return u && SAFE_URL.test(u) ? u : null;
}

// An image inside a link must not become a second, nested anchor.
const InsideLink = createContext(false);

// Lets the page that renders trusted text of its own (the help guide) turn a
// link into something else, such as an in-app route. It sees the href as
// written; null means "not mine", and the link then goes through the same
// checks as any other.
export type RenderLink = (href: string, children: ReactNode) => ReactNode | null;
const LinkRenderer = createContext<RenderLink | undefined>(undefined);

function SafeLink({ href, children }: ComponentProps<"a">): ReactNode {
  const renderLink = useContext(LinkRenderer);
  const custom = href && renderLink ? renderLink(href, children) : null;
  if (custom) return custom;
  const url = safeUrl(href);
  if (!url) return <span>{children}</span>;
  return (
    <a
      href={url}
      // The real destination, so link text can't disguise it.
      title={url}
      target="_blank"
      rel="noopener noreferrer"
    >
      <InsideLink.Provider value>{children}</InsideLink.Provider>
    </a>
  );
}

// An image is a request to somewhere the author chose, not the reader, so it
// is never fetched: it shows as a link to the image, labelled by its alt text.
function ImageAsLink({ src, alt }: ComponentProps<"img">): ReactNode {
  const inLink = useContext(InsideLink);
  const url = typeof src === "string" ? safeUrl(src) : null;
  const label = alt || url || "";
  if (!url || inLink) return label ? <span>{label}</span> : null;
  return (
    <a href={url} title={url} target="_blank" rel="noopener noreferrer">
      {label}
    </a>
  );
}

// GFM task lists render a disabled checkbox with no label of its own.
function TaskCheckbox({ checked, type }: ComponentProps<"input">): ReactNode {
  if (type !== "checkbox") return null;
  return <input type="checkbox" checked={checked} disabled readOnly aria-label={checked ? "done" : "not done"} />;
}

function codeSource(children: ReactNode): string {
  const code = Array.isArray(children) ? children[0] : children;
  const text = (code as { props?: { children?: unknown } } | null)?.props?.children;
  const raw = typeof text === "string" ? text : Array.isArray(text) ? text.join("") : "";
  // The parser ends a fenced block's text with a newline the author didn't write.
  return raw.endsWith("\n") ? raw.slice(0, -1) : raw;
}

function CodeBlock({ children }: ComponentProps<"pre">): ReactNode {
  // Which source was copied, so a block whose text changes under it (a live
  // transcript) stops saying "Copied" for text that was not.
  const [copiedSource, setCopiedSource] = useState<string | null>(null);
  const source = codeSource(children);
  const copied = copiedSource === source;
  async function copy() {
    try {
      await navigator.clipboard.writeText(source);
      setCopiedSource(source);
    } catch {
      setCopiedSource(null);
    }
  }
  return (
    <div className="markdown-view__code">
      <Button variant="ghost" size="sm" className="markdown-view__copy" onClick={copy}>
        {copied ? "Copied" : "Copy"}
      </Button>
      <pre>{children}</pre>
    </div>
  );
}

function ScrollTable({ children }: ComponentProps<"table">): ReactNode {
  return (
    <div className="markdown-view__scroll">
      <table>{children}</table>
    </div>
  );
}

const COMPONENTS: Components = { a: SafeLink, img: ImageAsLink, input: TaskCheckbox, pre: CodeBlock, table: ScrollTable };
const REMARK_PLUGINS = [remarkGfm];

export interface MarkdownViewProps {
  text: string;
  // Shows the text as it is: pre-wrapped and escaped, no markdown.
  plain?: boolean;
  className?: string;
  renderLink?: RenderLink;
}

// Memoised on its props, so re-rendering a long list does not re-parse the
// items whose text has not changed.
const MarkdownView = memo(function MarkdownView({ text, plain = false, className, renderLink }: MarkdownViewProps) {
  const classes = ["markdown-view", plain && "markdown-view--plain", className].filter(Boolean).join(" ");
  if (plain) return <div className={classes}>{text}</div>;
  return (
    <div className={classes}>
      <LinkRenderer.Provider value={renderLink}>
        <ReactMarkdown
          skipHtml
          remarkPlugins={REMARK_PLUGINS}
          components={COMPONENTS}
          // Unsafe schemes are still refused by SafeLink and ImageAsLink; this
          // only lets renderLink see a relative href instead of "".
          urlTransform={(url) => (renderLink ? url : (safeUrl(url) ?? ""))}
        >
          {text}
        </ReactMarkdown>
      </LinkRenderer.Provider>
    </div>
  );
});

export default MarkdownView;
