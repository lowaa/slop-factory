import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import LegalPage from "./legal_page";
import type { LegalDocsApi, LegalDocument } from "./types";

const href = (kind: string) => `/legal/${kind}`;

const doc: LegalDocument = {
  kind: "terms", version: "v1", locale: "en", effective: "2026-01-01", material: true,
  reviewed: false, current: true, body: "# Synthetic terms",
};

// A fake decides nothing: it returns what the test gives it and records the calls.
function fakeApi() {
  const calls: Array<[string, string | undefined]> = [];
  const api: LegalDocsApi = {
    getDocument: async (kind, version) => {
      calls.push([kind, version]);
      return doc;
    },
    getHistory: async () => ({ kind: "terms", locale: "en", versions: [] }),
  };
  return { api, calls };
}

describe("LegalPage", () => {
  it("loads the document through the api it is given", async () => {
    const { api, calls } = fakeApi();
    render(<LegalPage kind="terms" version="v1" href={href} api={api} />);
    expect(await screen.findByRole("heading", { name: "Synthetic terms" })).toBeInTheDocument();
    expect(calls).toEqual([["terms", "v1"]]);
  });

  it("shows the back link only when one is given", async () => {
    const { api } = fakeApi();
    const { rerender } = render(<LegalPage kind="terms" href={href} api={api} />);
    await screen.findByRole("heading", { name: "Synthetic terms" });
    expect(screen.queryByRole("link", { name: "Back home" })).toBeNull();

    rerender(<LegalPage kind="terms" href={href} api={api} backLink={{ href: "/", label: "Back home" }} />);
    expect(screen.getByRole("link", { name: "Back home" })).toHaveAttribute("href", "/");
  });

  it("puts the app's class on the main landmark", async () => {
    const { api } = fakeApi();
    render(<LegalPage kind="terms" href={href} api={api} className="mine" />);
    await screen.findByRole("heading", { name: "Synthetic terms" });
    expect(screen.getByRole("main")).toHaveClass("mine");
  });
});
