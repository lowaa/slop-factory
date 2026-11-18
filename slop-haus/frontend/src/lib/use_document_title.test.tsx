import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { useDocumentTitle, useTenantTitlePrefix } from "./use_document_title";

function Page({ title, prefix }: { title?: string | null; prefix?: string | null }) {
  useTenantTitlePrefix(prefix ?? null);
  useDocumentTitle(title ?? null);
  return null;
}

describe("document title", () => {
  it("prefixes the page's title with the tenant", () => {
    const { unmount } = render(<Page title="Board" prefix="Acme Web" />);
    expect(document.title).toBe("Acme Web · Board");
    unmount();
    expect(document.title).toBe("slop-haus");
  });

  it("prefixes the default title for a page that sets none", () => {
    const { unmount } = render(<Page prefix="Acme Web" />);
    expect(document.title).toBe("Acme Web · slop-haus");
    unmount();
  });

  it("leaves the title bare without a tenant prefix", () => {
    const { unmount } = render(<Page title="Board" />);
    expect(document.title).toBe("Board");
    unmount();
  });

  it("follows a prefix that arrives after the page's title", () => {
    const { rerender, unmount } = render(<Page title="Board" />);
    rerender(<Page title="Board" prefix="Lab" />);
    expect(document.title).toBe("Lab · Board");
    unmount();
  });
});
