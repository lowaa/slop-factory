import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import MarkdownView from "./markdown_view";

function renderMd(text: string, plain = false) {
  return render(<MarkdownView text={text} plain={plain} />);
}

describe("MarkdownView", () => {
  it("renders gfm: headings, tables, task lists, strikethrough", () => {
    const { container } = renderMd("# T\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n- [x] done\n\n~~gone~~");
    expect(screen.getByRole("heading", { level: 1, name: "T" })).toBeInTheDocument();
    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(container.querySelector("input[type=checkbox]")).toBeInTheDocument();
    expect(container.querySelector("del")).toBeInTheDocument();
  });

  it("labels task list checkboxes", () => {
    renderMd("- [x] done thing\n- [ ] todo thing");
    expect(screen.getByRole("checkbox", { name: "done" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "not done" })).not.toBeChecked();
  });

  it("renders nothing executable from hostile input", () => {
    const { container } = renderMd(
      [
        "<script>window.__pwned = true</script>",
        '<img src=x onerror="window.__pwned = true">',
        '<a href="javascript:window.__pwned=true">raw</a>',
        "[md](javascript:window.__pwned=true)",
        '<div onclick="x()">raw</div>',
      ].join("\n\n"),
    );
    expect(container.querySelector("script, img, [onerror], [onclick]")).toBeNull();
    expect(container.querySelector("a[href^='javascript' i]")).toBeNull();
    expect(container.innerHTML).not.toMatch(/javascript:/i);
    expect((window as unknown as { __pwned?: boolean }).__pwned).toBeUndefined();
  });

  it("renders links with an http, https or mailto scheme only, as safe new-tab links", () => {
    renderMd("[a](https://example.com/x) [b](http://example.com) [c](mailto:x@example.com)");
    for (const name of ["a", "b", "c"]) {
      const link = screen.getByRole("link", { name });
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAttribute("rel", "noopener noreferrer");
      expect(link).toHaveAttribute("title", link.getAttribute("href"));
    }
  });

  it.each(["javascript:alert(1)", "data:text/html,x", "tel:123", "/relative", "//evil.example", "ftp://x"])(
    "renders a %s link as text",
    (href) => {
      renderMd(`[label](${href})`);
      expect(screen.queryByRole("link")).toBeNull();
      expect(screen.getByText("label")).toBeInTheDocument();
    },
  );

  it("renders an image as a link to it, never an img", () => {
    const { container } = renderMd("![a chart](https://example.com/c.png)");
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByRole("link", { name: "a chart" })).toHaveAttribute("href", "https://example.com/c.png");
  });

  it("does not nest an anchor for an image inside a link", () => {
    const { container } = renderMd("[![alt](https://example.com/i.png)](https://example.com)");
    expect(container.querySelectorAll("a")).toHaveLength(1);
    expect(container.querySelector("a a")).toBeNull();
    expect(screen.getByRole("link", { name: "alt" })).toHaveAttribute("href", "https://example.com");
  });

  it("copies a code block's exact source", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    renderMd("```\nline 1\n  line 2 <b>\n```");
    await user.click(screen.getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith("line 1\n  line 2 <b>");
    expect(await screen.findByRole("button", { name: "Copied" })).toBeInTheDocument();
  });

  it("keeps inline code out of the copy button", () => {
    renderMd("some `code` here");
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("plain mode shows the text escaped and unparsed", () => {
    const text = "# not a heading\n<b>raw</b> [x](https://example.com)";
    const { container } = renderMd(text, true);
    expect(container.querySelector("h1, b, a")).toBeNull();
    expect(container.firstElementChild?.textContent).toBe(text);
    expect(container.firstElementChild).toHaveClass("markdown-view--plain");
  });

  describe("renderLink", () => {
    const renderLink = (href: string, children: React.ReactNode) =>
      href.startsWith("page:") ? <button type="button">{children}</button> : null;

    it("lets the caller take a link over", () => {
      render(<MarkdownView text="[go](page:one)" renderLink={renderLink} />);
      expect(screen.getByRole("button", { name: "go" })).toBeTruthy();
    });

    it("leaves other links on the usual checks", () => {
      render(<MarkdownView text="[ok](https://example.com) [bad](javascript:alert(1))" renderLink={renderLink} />);
      expect(screen.getByRole("link", { name: "ok" }).getAttribute("href")).toBe("https://example.com");
      expect(screen.queryByRole("link", { name: "bad" })).toBeNull();
    });
  });
});
