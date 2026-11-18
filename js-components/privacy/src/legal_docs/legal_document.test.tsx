import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import LegalDocumentView from "./legal_document";
import type { LegalDocHref, LegalDocument, LegalHistory } from "./types";

const href: LegalDocHref = (kind, version) => `/legal/${kind}${version ? `/${version}` : ""}`;

function doc(over: Partial<LegalDocument> = {}): LegalDocument {
  return {
    kind: "terms",
    version: "v2",
    locale: "en",
    effective: "2026-02-01",
    material: true,
    reviewed: false,
    current: true,
    body: "# Synthetic terms\n\nBody.",
    ...over,
  };
}

const history: LegalHistory = {
  kind: "terms",
  locale: "en",
  versions: [
    { version: "v2", effective: "2026-02-01", material: true, reviewed: false, current: true },
    { version: "v1", effective: "2026-01-01", material: true, reviewed: true, reviewed_on: "2026-01-01", current: false },
  ],
};

describe("LegalDocumentView", () => {
  it("shows the DRAFT banner while the text is not reviewed", () => {
    render(<LegalDocumentView document={doc({ reviewed: false })} href={href} />);
    expect(screen.getByRole("note")).toHaveTextContent("DRAFT – not legally reviewed");
  });

  it("clears the banner once reviewed text is published", () => {
    render(
      <LegalDocumentView
        document={doc({ reviewed: true, reviewed_by: "Synthetic Reviewer", reviewed_on: "2026-02-01" })}
        href={href}
      />,
    );
    expect(screen.queryByRole("note")).toBeNull();
    expect(screen.queryByText(/DRAFT/)).toBeNull();
    expect(screen.getByText(/reviewed 2026-02-01/)).toBeInTheDocument();
  });

  it("renders the body", () => {
    render(<LegalDocumentView document={doc()} href={href} />);
    expect(screen.getByRole("heading", { name: "Synthetic terms" })).toBeInTheDocument();
  });

  it("links the version history, marking the current and draft versions", () => {
    render(<LegalDocumentView document={doc()} history={history} href={href} />);
    const nav = screen.getByRole("navigation", { name: "Version history" });
    const items = within(nav).getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(within(items[0]).getByRole("link", { name: "v2" })).toHaveAttribute("href", "/legal/terms/v2");
    expect(items[0]).toHaveTextContent("current");
    expect(items[0]).toHaveTextContent("draft");
    expect(within(items[1]).getByRole("link", { name: "v1" })).toHaveAttribute("href", "/legal/terms/v1");
    expect(items[1]).not.toHaveTextContent("draft");
  });

  it("points an older version at the current one", () => {
    render(<LegalDocumentView document={doc({ version: "v1", current: false })} href={href} />);
    expect(screen.getByRole("link", { name: "read the current version" })).toHaveAttribute("href", "/legal/terms");
  });

  it("has no history nav without a history", () => {
    render(<LegalDocumentView document={doc()} href={href} />);
    expect(screen.queryByRole("navigation", { name: "Version history" })).toBeNull();
  });

  it("does not execute a script in a body", () => {
    const { container } = render(
      <LegalDocumentView document={doc({ body: "Hi\n\n<script>window.__pwned = true</script>" })} href={href} />,
    );
    expect(container.querySelector("script")).toBeNull();
  });
});
