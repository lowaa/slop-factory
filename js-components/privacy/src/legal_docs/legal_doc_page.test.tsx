import { act, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import LegalDocPage from "./legal_doc_page";
import { NotFoundError } from "./fetch_api";
import type { LegalDocsApi, LegalDocument, LegalHistory } from "./types";

const href = (kind: string, version?: string) => `/legal/${kind}${version ? `/${version}` : ""}`;

const document_: LegalDocument = {
  kind: "terms", version: "v1", locale: "en", effective: "2026-01-01", material: true,
  reviewed: false, current: true, body: "# Synthetic terms",
};
const history: LegalHistory = {
  kind: "terms", locale: "en",
  versions: [{ version: "v1", effective: "2026-01-01", material: true, reviewed: false, current: true }],
};

// A fake decides nothing: it returns what the test gives it and records the calls.
function fakeApi(over: Partial<LegalDocsApi> = {}) {
  const calls: Array<[string, string | undefined]> = [];
  const api: LegalDocsApi = {
    getDocument: async (kind, version) => {
      calls.push([kind, version]);
      return document_;
    },
    getHistory: async () => history,
    ...over,
  };
  return { api, calls };
}

describe("LegalDocPage", () => {
  it("loads the document and shows it with its history and banner", async () => {
    const { api, calls } = fakeApi();
    render(<LegalDocPage api={api} kind="terms" href={href} />);
    expect(screen.getByRole("status")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Synthetic terms" })).toBeInTheDocument();
    expect(screen.getByRole("note")).toHaveTextContent("DRAFT – not legally reviewed");
    expect(screen.getByRole("navigation", { name: "Version history" })).toBeInTheDocument();
    expect(calls).toEqual([["terms", undefined]]);
  });

  it("asks for the exact version when one is given", async () => {
    const { api, calls } = fakeApi();
    render(<LegalDocPage api={api} kind="terms" version="v1" href={href} />);
    await screen.findByRole("heading", { name: "Synthetic terms" });
    expect(calls).toEqual([["terms", "v1"]]);
  });

  it("says so when the document does not exist", async () => {
    const { api } = fakeApi({ getDocument: async () => { throw new NotFoundError(); } });
    render(<LegalDocPage api={api} kind="terms" href={href} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("not available");
  });

  it("says so when loading fails", async () => {
    const { api } = fakeApi({ getDocument: async () => { throw new Error("boom"); } });
    render(<LegalDocPage api={api} kind="terms" href={href} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be loaded");
    expect(screen.queryByText("boom")).toBeNull();
  });

  it("shows the text without a history when only the history fails", async () => {
    const { api } = fakeApi({ getHistory: async () => { throw new Error("boom"); } });
    render(<LegalDocPage api={api} kind="terms" href={href} />);
    expect(await screen.findByRole("heading", { name: "Synthetic terms" })).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "Version history" })).toBeNull();
  });

  it("loads again when the version changes", async () => {
    const { api, calls } = fakeApi();
    const { rerender } = render(<LegalDocPage api={api} kind="terms" version="v1" href={href} />);
    await screen.findByRole("heading", { name: "Synthetic terms" });
    rerender(<LegalDocPage api={api} kind="terms" version="v2" href={href} />);
    await screen.findByRole("heading", { name: "Synthetic terms" });
    expect(calls).toEqual([["terms", "v1"], ["terms", "v2"]]);
  });
});

describe("LegalDocPage with overlapping requests", () => {
  it("shows the latest version, not whichever answer arrives last", async () => {
    const pending = new Map<string, (d: LegalDocument) => void>();
    const { api } = fakeApi({
      getDocument: (_kind, version) => new Promise<LegalDocument>((resolve) => pending.set(version ?? "", resolve)),
    });
    const { rerender } = render(<LegalDocPage api={api} kind="terms" version="v1" href={href} />);
    rerender(<LegalDocPage api={api} kind="terms" version="v2" href={href} />);

    pending.get("v2")?.({ ...document_, version: "v2", body: "# Second" });
    await screen.findByRole("heading", { name: "Second" });
    pending.get("v1")?.({ ...document_, version: "v1", body: "# First" });
    await act(async () => {});
    expect(screen.getByRole("heading", { name: "Second" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "First" })).toBeNull();
  });
});
