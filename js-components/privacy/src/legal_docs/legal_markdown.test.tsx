import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import LegalMarkdown from "./legal_markdown";

function renderMd(source: string) {
  return render(<LegalMarkdown source={source} />);
}

describe("LegalMarkdown", () => {
  it("renders headings, lists and tables", () => {
    renderMd("# Title\n\n## Section\n\n- one\n- two\n\n| a | b |\n|---|---|\n| 1 | 2 |\n");
    expect(screen.getByRole("heading", { level: 1, name: "Title" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Section" })).toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    expect(screen.getByRole("table")).toBeInTheDocument();
  });

  it("does not render raw HTML, scripts included", () => {
    const { container } = renderMd(
      'Before\n\n<script>window.__pwned = true</script>\n\n<img src=x onerror="window.__pwned = true">\n\n<div onclick="x()">raw</div>\n\nAfter',
    );
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("[onclick]")).toBeNull();
    expect(container.innerHTML).not.toContain("__pwned");
    expect((window as unknown as { __pwned?: boolean }).__pwned).toBeUndefined();
    expect(screen.getByText("Before")).toBeInTheDocument();
    expect(screen.getByText("After")).toBeInTheDocument();
  });

  it.each([
    ["javascript:", "[click](javascript:alert(1))"],
    ["mixed case", "[click](JaVaScRiPt:alert(1))"],
    ["entity-encoded", "[click](&#106;avascript:alert(1))"],
    ["leading whitespace", "[click](<  javascript:alert(1)>)"],
    ["data:", "[click](data:text/html;base64,PHNjcmlwdD4=)"],
    ["vbscript:", "[click](vbscript:x)"],
    ["scheme-relative", "[click](//evil.example/x)"],
    ["a reference link", "[click][r]\n\n[r]: javascript:alert(1)"],
    ["an autolink", "<javascript:alert(1)>"],
  ])("does not make a link of a %s URL", (_name, source) => {
    const { container } = renderMd(source);
    expect(screen.queryByRole("link")).toBeNull();
    expect(container.querySelector("a")).toBeNull();
    expect(container.innerHTML).not.toMatch(/href=/i);
  });

  it("keeps a backslash from turning a path into another host", () => {
    renderMd("[click](/\\evil.example/x)");
    // Either not a link, or encoded so a browser reads it as part of the path.
    const href = screen.queryByRole("link")?.getAttribute("href") ?? "";
    expect(href).not.toContain("\\");
    expect(href === "" || href.startsWith("/%5C")).toBe(true);
  });

  it("keeps the text of a refused link", () => {
    renderMd("[click here](javascript:alert(1))");
    expect(screen.getByText("click here")).toBeInTheDocument();
  });

  it("links to https, mailto and same-site addresses", () => {
    renderMd("[a](https://example.test/x) [b](mailto:privacy@example.test) [c](/privacy/terms) [d](#section)");
    expect(screen.getByRole("link", { name: "a" })).toHaveAttribute("href", "https://example.test/x");
    expect(screen.getByRole("link", { name: "b" })).toHaveAttribute("href", "mailto:privacy@example.test");
    expect(screen.getByRole("link", { name: "c" })).toHaveAttribute("href", "/privacy/terms");
    expect(screen.getByRole("link", { name: "d" })).toHaveAttribute("href", "#section");
  });

  it("opens external links in a new tab without handing it the page", () => {
    renderMd("[a](https://example.test/x) [c](/privacy/terms)");
    const ext = screen.getByRole("link", { name: "a" });
    expect(ext).toHaveAttribute("target", "_blank");
    expect(ext).toHaveAttribute("rel", "noopener noreferrer");
    expect(screen.getByRole("link", { name: "c" })).not.toHaveAttribute("target");
  });

  it("does not load images, and keeps their description", () => {
    const { container } = renderMd("![a tracking pixel](https://tracker.example/p.gif)");
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByText("a tracking pixel")).toBeInTheDocument();
  });

  it("marks TODO-LEGAL gaps so they stand out", () => {
    const { container } = renderMd("Intro [[TODO-LEGAL: who the controller is]] and more.");
    const marks = container.querySelectorAll("mark");
    expect(marks).toHaveLength(1);
    expect(marks[0]).toHaveTextContent("TODO: who the controller is");
    expect(container).toHaveTextContent("Intro TODO: who the controller is and more.");
  });

  it("marks a gap inside a list item and a heading", () => {
    const { container } = renderMd("## Heading [[TODO-LEGAL: a]]\n\n- item [[TODO-LEGAL: b]]\n");
    expect(container.querySelectorAll("mark")).toHaveLength(2);
  });
});
