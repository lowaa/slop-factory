import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { LegalDocsApi, LegalDocument } from "@slop-factory/privacy/legal_docs";
import { ALL_REFUSED, FRESH_VISIT, makeConsentStore, stubConsentApi } from "@/test/consent";
import { ConsentStore } from "@slop-factory/privacy/consent";
import { WithConsent } from "@/test/with_consent";
import AppFooter from "./app_footer";
import LegalPage from "./legal_page";
import { legalDocHref } from "./legal_links";

function renderFooter(store: ConsentStore) {
  return render(
    <WithConsent store={store}>
      <AppFooter />
    </WithConsent>,
  );
}

describe("the footer", () => {
  it("links every published document and offers the cookie preferences", async () => {
    renderFooter(makeConsentStore());
    const nav = await screen.findByRole("navigation", { name: "Legal" });

    // Pinned here rather than read from LEGAL_LINKS, so dropping a document
    // or changing where it lives fails this.
    expect(within(nav).getAllByRole("link").map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
      ["Privacy policy", "/legal/privacy_policy"],
      ["Cookie policy", "/legal/cookie_policy"],
      ["Terms", "/legal/terms"],
      ["Notice at collection", "/legal/ccpa_notice_at_collection"],
    ]);
    expect(legalDocHref("terms", "2026-10-03.1")).toBe("/legal/terms/2026-10-03.1");
    expect(within(nav).getByRole("button", { name: "Cookie preferences" })).toBeInTheDocument();
    expect(within(nav).queryByText("Health data notice")).not.toBeInTheDocument();
  });

  it("opts out of sale and sharing, recording marketing as refused and nothing else", async () => {
    const api = stubConsentApi(FRESH_VISIT);
    const put = vi.spyOn(api, "put");
    renderFooter(new ConsentStore({ api, name: "t", locks: null, createChannel: null }));

    await userEvent.click(await screen.findByRole("button", { name: "Do Not Sell or Share My Personal Information" }));

    await waitFor(() => expect(put).toHaveBeenCalled());
    expect(put.mock.calls[0][0]).toBe("settings");
    expect(put.mock.calls[0][1]).toEqual([{ purpose: "marketing", granted: false }]);
    expect(await screen.findByText("Your choice was saved.")).toBeInTheDocument();
  });

  // The entry has to be on the page whatever the server offers: a missing
  // link is a CCPA failure, not a tidy UI.
  it("still shows Do Not Sell or Share when no sale-or-share purpose is offered", async () => {
    const api = stubConsentApi({ ...FRESH_VISIT, purposes: FRESH_VISIT.purposes.filter((p) => p.id !== "marketing") });
    const put = vi.spyOn(api, "put");
    renderFooter(new ConsentStore({ api, name: "t", locks: null, createChannel: null }));

    await userEvent.click(await screen.findByRole("button", { name: "Do Not Sell or Share My Personal Information" }));

    expect(await screen.findByText(/nothing to turn off/)).toBeInTheDocument();
    expect(put).not.toHaveBeenCalled();
  });
});

describe("the banner", () => {
  it("shows on a first visit, and Reject all refuses every optional purpose", async () => {
    const api = stubConsentApi(FRESH_VISIT);
    const put = vi.spyOn(api, "put");
    render(
      <WithConsent store={new ConsentStore({ api, name: "t", locks: null, createChannel: null })}>
        <p>page</p>
      </WithConsent>,
    );

    await userEvent.click(await screen.findByRole("button", { name: "Reject all" }));

    await waitFor(() => expect(put).toHaveBeenCalled());
    expect(put.mock.calls[0][1]).toEqual([
      { purpose: "analytics", granted: false },
      { purpose: "marketing", granted: false },
    ]);
    await waitFor(() => expect(screen.queryByRole("button", { name: "Reject all" })).not.toBeInTheDocument());
  });

  it("is not shown once the visitor has decided", async () => {
    render(
      <WithConsent store={makeConsentStore(ALL_REFUSED)}>
        <p>page</p>
      </WithConsent>,
    );
    await screen.findByText("page");
    await waitFor(() => expect(screen.queryByRole("button", { name: "Accept all" })).not.toBeInTheDocument());
  });
});

const DRAFT: LegalDocument = {
  kind: "privacy_policy",
  version: "2026-10-03.1",
  locale: "en",
  effective: "2026-10-03",
  material: true,
  reviewed: false,
  current: true,
  body: "# Privacy policy\n\nSome text.",
};

describe("a legal page", () => {
  it("shows the document with its DRAFT banner", async () => {
    const api: LegalDocsApi = {
      getDocument: async () => DRAFT,
      getHistory: async () => ({ kind: "privacy_policy", locale: "en", versions: [] }),
    };
    render(<LegalPage kind="privacy_policy" api={api} />);

    expect(await screen.findByRole("heading", { name: "Privacy policy" })).toBeInTheDocument();
    expect(screen.getByText(/DRAFT/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to slop-haus" })).toHaveAttribute("href", "/");
  });
});
