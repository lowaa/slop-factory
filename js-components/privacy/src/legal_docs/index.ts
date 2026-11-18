export { default as LegalDocPage } from "./legal_doc_page";
export { default as LegalPage } from "./legal_page";
export { default as LegalDocumentView } from "./legal_document";
export { default as LegalMarkdown } from "./legal_markdown";
export { default as DraftBanner } from "./draft_banner";
export { default as LegalFooterLinks, DEFAULT_LEGAL_LINKS, type LegalLink } from "./legal_footer_links";
export { createFetchLegalDocsApi, NotFoundError, DEFAULT_DOCUMENTS_PATH } from "./fetch_api";
export type {
  LegalDocument,
  LegalVersionSummary,
  LegalHistory,
  LegalDocsApi,
  LegalDocHref,
} from "./types";
