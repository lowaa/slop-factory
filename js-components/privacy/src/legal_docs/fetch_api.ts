import type { LegalDocsApi, LegalDocument, LegalHistory } from "./types";

export const DEFAULT_DOCUMENTS_PATH = "/api/privacy/documents";

export class NotFoundError extends Error {
  constructor() {
    super("document not found");
    this.name = "NotFoundError";
  }
}

interface FetchApiOptions {
  basePath?: string;
  locale?: string;
  // For tests; the global fetch otherwise.
  fetchFn?: typeof fetch;
}

// The documents are public, so these requests carry no credentials.
export function createFetchLegalDocsApi({
  basePath = DEFAULT_DOCUMENTS_PATH,
  locale,
  fetchFn,
}: FetchApiOptions = {}): LegalDocsApi {
  async function get<T>(path: string): Promise<T> {
    const query = locale ? `?locale=${encodeURIComponent(locale)}` : "";
    const res = await (fetchFn ?? fetch)(`${basePath}${path}${query}`, {
      credentials: "omit",
      headers: { Accept: "application/json" },
    });
    if (res.status === 404) throw new NotFoundError();
    if (!res.ok) throw new Error(`documents request failed (${res.status})`);
    return (await res.json()) as T;
  }
  return {
    getDocument: (kind, version) =>
      get<LegalDocument>(
        version === undefined
          ? `/${encodeURIComponent(kind)}`
          : `/${encodeURIComponent(kind)}/versions/${encodeURIComponent(version)}`,
      ),
    getHistory: (kind) => get<LegalHistory>(`/${encodeURIComponent(kind)}/versions`),
  };
}
