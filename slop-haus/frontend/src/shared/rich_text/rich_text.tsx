import { createContext, memo, useContext, useState, type ReactNode } from "react";
import { Button, MarkdownView } from "@slop-factory/ui";
import type { RenderMarkdown } from "@/lib/markdown_setting";
import "./rich_text.scss";

// Whether text under it renders as markdown. Rendered unless a page says
// otherwise, which is the setting's default.
const RenderContext = createContext(true);

export function RichTextMode({ render, children }: { render: boolean; children: ReactNode }) {
  return <RenderContext.Provider value={render}>{children}</RenderContext.Provider>;
}

function CopySource({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }
  return (
    <Button variant="ghost" size="sm" className="rich-text__copy" onClick={copy}>
      {copied ? "Copied" : "Copy"}
    </Button>
  );
}

// Text written by the board's own users and agents, as markdown or as it was
// typed. Never for EXTERNAL CONTENT, which stays escaped text whatever the
// setting. Memoised, so a long list does not re-parse the items that did not
// change when one is added.
const RichText = memo(function RichText({ text, className }: { text: string; className?: string }) {
  const render = useContext(RenderContext);
  if (render) return <MarkdownView text={text} className={className} />;
  return (
    <div className="rich-text__plain">
      <MarkdownView plain text={text} className={className} />
      <CopySource text={text} />
    </div>
  );
});

export default RichText;

// The page's own "Rendered / Plain" flip. It changes this page only; the
// saved default lives in Your settings.
export function RichTextToggle({ markdown }: { markdown: RenderMarkdown }) {
  return (
    <div className="rich-text__toggle" role="group" aria-label="Text view">
      <Button size="sm" variant={markdown.value ? "primary" : "ghost"} aria-pressed={markdown.value} onClick={() => !markdown.value && markdown.toggleForPage()}>
        Rendered
      </Button>
      <Button size="sm" variant={markdown.value ? "ghost" : "primary"} aria-pressed={!markdown.value} onClick={() => markdown.value && markdown.toggleForPage()}>
        Plain
      </Button>
    </div>
  );
}
