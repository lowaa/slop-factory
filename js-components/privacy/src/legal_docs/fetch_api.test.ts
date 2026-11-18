import { describe, expect, it } from "vitest";
import { createFetchLegalDocsApi, NotFoundError } from "./fetch_api";

function fakeFetch(status: number, body: unknown = {}) {
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  const fetchFn = (async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    return new Response(JSON.stringify(body), { status });
  }) as unknown as typeof fetch;
  return { fetchFn, calls };
}

describe("createFetchLegalDocsApi", () => {
  it("asks for the current version, the exact one and the history", async () => {
    const { fetchFn, calls } = fakeFetch(200);
    const api = createFetchLegalDocsApi({ fetchFn });
    await api.getDocument("privacy_policy");
    await api.getDocument("privacy_policy", "2026-01-01.1");
    await api.getHistory("privacy_policy");
    expect(calls.map((c) => c.url)).toEqual([
      "/api/privacy/documents/privacy_policy",
      "/api/privacy/documents/privacy_policy/versions/2026-01-01.1",
      "/api/privacy/documents/privacy_policy/versions",
    ]);
  });

  it("sends no credentials: the documents are public", async () => {
    const { fetchFn, calls } = fakeFetch(200);
    await createFetchLegalDocsApi({ fetchFn }).getDocument("terms");
    expect(calls[0].init?.credentials).toBe("omit");
  });

  it("passes the locale and escapes what it puts in a path", async () => {
    const { fetchFn, calls } = fakeFetch(200);
    const api = createFetchLegalDocsApi({ fetchFn, locale: "fr", basePath: "/x" });
    await api.getDocument("a/b", "v 1");
    expect(calls[0].url).toBe("/x/a%2Fb/versions/v%201?locale=fr");
  });

  it("throws NotFoundError for a 404 and a plain error otherwise", async () => {
    await expect(createFetchLegalDocsApi({ fetchFn: fakeFetch(404).fetchFn }).getDocument("terms")).rejects.toBeInstanceOf(
      NotFoundError,
    );
    const err = await createFetchLegalDocsApi({ fetchFn: fakeFetch(500).fetchFn })
      .getDocument("terms")
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(Error);
    expect(err).not.toBeInstanceOf(NotFoundError);
  });

  it("returns the body as the server sent it", async () => {
    const { fetchFn } = fakeFetch(200, { kind: "terms", version: "v1" });
    expect(await createFetchLegalDocsApi({ fetchFn }).getDocument("terms")).toEqual({ kind: "terms", version: "v1" });
  });
});
