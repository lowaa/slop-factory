// What go-privacy's legaldocs routes return (legaldocs/routes.go).

export interface LegalDocument {
  kind: string;
  version: string;
  locale: string;
  // YYYY-MM-DD
  effective: string;
  material: boolean;
  // False for placeholder text: the page shows a DRAFT banner.
  reviewed: boolean;
  reviewed_by?: string;
  reviewed_on?: string;
  // Whether this is the version in force.
  current: boolean;
  // Markdown. Untrusted: render it with LegalMarkdown.
  body: string;
}

export interface LegalVersionSummary {
  version: string;
  effective: string;
  material: boolean;
  reviewed: boolean;
  reviewed_on?: string;
  current: boolean;
}

export interface LegalHistory {
  kind: string;
  locale: string;
  // Newest first.
  versions: LegalVersionSummary[];
}

export interface LegalDocsApi {
  getDocument(kind: string, version?: string): Promise<LegalDocument>;
  getHistory(kind: string): Promise<LegalHistory>;
}

// The page for a kind's version, or the kind's current one when version is
// undefined. Apps own their routes, so they say where the links go.
export type LegalDocHref = (kind: string, version?: string) => string;
