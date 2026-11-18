import type { LegalDocHref, LegalLink } from "@slop-factory/privacy/legal_docs";

export const LEGAL_PATH = "/legal";

// The documents slop-haus publishes. No health data notice: it holds no
// health data.
export const LEGAL_LINKS: readonly LegalLink[] = [
  { kind: "privacy_policy", label: "Privacy policy" },
  { kind: "cookie_policy", label: "Cookie policy" },
  { kind: "terms", label: "Terms" },
  { kind: "ccpa_notice_at_collection", label: "Notice at collection" },
];

export const legalDocHref: LegalDocHref = (kind, version) =>
  version === undefined
    ? `${LEGAL_PATH}/${encodeURIComponent(kind)}`
    : `${LEGAL_PATH}/${encodeURIComponent(kind)}/${encodeURIComponent(version)}`;
