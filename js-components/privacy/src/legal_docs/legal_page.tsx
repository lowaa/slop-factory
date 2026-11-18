import { useMemo } from "react";
import LegalDocPage from "./legal_doc_page";
import { createFetchLegalDocsApi } from "./fetch_api";
import type { LegalDocHref, LegalDocsApi } from "./types";
import "./legal_page.scss";

interface LegalPageProps {
  kind: string;
  // Undefined is the version in force.
  version?: string;
  href: LegalDocHref;
  // Overridable for tests; the real client otherwise.
  api?: LegalDocsApi;
  // The app's own layout class for the page's <main>.
  className?: string;
  backLink?: { href: string; label: string };
}

// A legal document on its own page. Public: it needs no session, so a
// visitor can read a policy before choosing anything.
export default function LegalPage({ kind, version, href, api, className, backLink }: LegalPageProps) {
  const client = useMemo(() => api ?? createFetchLegalDocsApi(), [api]);
  return (
    <main className={className}>
      {backLink && (
        <a className="legal-page-back" href={backLink.href}>
          {backLink.label}
        </a>
      )}
      <LegalDocPage api={client} kind={kind} version={version} href={href} />
    </main>
  );
}
