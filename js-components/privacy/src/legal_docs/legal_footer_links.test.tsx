import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import LegalFooterLinks from "./legal_footer_links";

const href = (kind: string) => `/legal/${kind}`;

describe("LegalFooterLinks", () => {
  it("links every built-in kind by default", () => {
    render(<LegalFooterLinks href={href} />);
    const nav = screen.getByRole("navigation", { name: "Legal" });
    expect(nav).toBeInTheDocument();
    expect(screen.getAllByRole("link").map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
      ["Privacy policy", "/legal/privacy_policy"],
      ["Cookie policy", "/legal/cookie_policy"],
      ["Terms", "/legal/terms"],
      ["Health data notice", "/legal/health_data_notice"],
      ["Notice at collection", "/legal/ccpa_notice_at_collection"],
    ]);
  });

  it("links only what an app lists, and takes extra children", () => {
    render(
      <LegalFooterLinks
        href={href}
        links={[{ kind: "terms", label: "Terms" }, { kind: "beta_terms", label: "Beta terms" }]}
      >
        <button type="button">Cookie settings</button>
      </LegalFooterLinks>,
    );
    expect(screen.getAllByRole("link").map((a) => a.textContent)).toEqual(["Terms", "Beta terms"]);
    expect(screen.getByRole("button", { name: "Cookie settings" })).toBeInTheDocument();
  });

  it("puts each child in its own list item, so the list's gap separates them", () => {
    render(
      <LegalFooterLinks href={href} links={[]}>
        <a href="/request">Privacy request</a>
        <button type="button">Cookie preferences</button>
        <span>
          <button type="button">Do Not Sell</button>
          <span>saved</span>
        </span>
      </LegalFooterLinks>,
    );
    const items = screen.getAllByRole("listitem");
    expect(items.map((li) => li.textContent)).toEqual(["Privacy request", "Cookie preferences", "Do Not Sellsaved"]);
  });
});
