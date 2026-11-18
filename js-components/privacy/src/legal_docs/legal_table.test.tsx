import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import LegalMarkdown from "./legal_markdown";
import { cookieTable, subProcessors } from "./legal_table_fixtures";

describe("LegalTable", () => {
  it("labels every cell with its column's header", () => {
    render(<LegalMarkdown source={subProcessors} />);
    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers).toHaveLength(7);
    const rows = screen.getAllByRole("row").slice(1);
    expect(rows.length).toBeGreaterThan(0);
    for (const row of rows) {
      const cells = within(row).getAllByRole("cell");
      expect(cells).toHaveLength(headers.length);
      cells.forEach((cell, i) => {
        expect(cell.querySelector(".legal-table-label")).toHaveTextContent(`${headers[i]}:`);
      });
    }
  });

  it("hides the visible labels from screen readers", () => {
    const { container } = render(<LegalMarkdown source={cookieTable} />);
    const labels = container.querySelectorAll(".legal-table-label");
    expect(labels.length).toBeGreaterThan(0);
    for (const l of labels) expect(l).toHaveAttribute("aria-hidden", "true");
  });

  it("is a labelled, focusable scroll region with explicit table roles", () => {
    const { container } = render(<LegalMarkdown source={cookieTable} />);
    const region = screen.getByRole("region", { name: /Name, Type, Purpose/ });
    expect(region).toHaveAttribute("tabindex", "0");
    expect(region.querySelector("table")).toHaveAttribute("role", "table");
    expect(container.querySelectorAll('[role="rowgroup"]')).toHaveLength(2);
  });

  it("keeps links in cells going through SafeLink", () => {
    render(
      <LegalMarkdown
        source={"| Name | Link |\n| --- | --- |\n| a | [ok](https://example.com) |\n| b | [bad](javascript:alert(1)) |\n"}
      />,
    );
    const ok = screen.getByRole("link", { name: "ok" });
    expect(ok).toHaveAttribute("rel", "noopener noreferrer");
    expect(ok).toHaveAttribute("target", "_blank");
    expect(screen.queryByRole("link", { name: "bad" })).toBeNull();
    expect(screen.getByText("bad")).toBeInTheDocument();
  });

  it("falls back to a plain scrolling table with no header text", () => {
    const { container } = render(<LegalMarkdown source={"|   |   |\n| --- | --- |\n| 1 | 2 |\n"} />);
    expect(container.querySelector(".legal-table-scroll-stackable")).toBeNull();
    expect(container.querySelector(".legal-table-label")).toBeNull();
    expect(screen.getByRole("region", { name: "Table" })).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
  });
});
