import DraftBanner from "./draft_banner";
import LegalMarkdown from "./legal_markdown";
import type { LegalDocHref, LegalDocument, LegalHistory } from "./types";
import "./legal_document.scss";

interface LegalDocumentViewProps {
  document: LegalDocument;
  history?: LegalHistory | null;
  href: LegalDocHref;
}

export default function LegalDocumentView({ document: doc, history, href }: LegalDocumentViewProps) {
  return (
    <article className="legal-document">
      {!doc.reviewed && <DraftBanner />}
      <LegalMarkdown source={doc.body} />
      <footer className="legal-document__meta">
        <p>
          Version {doc.version}, effective {doc.effective}
          {doc.reviewed && doc.reviewed_on ? `, reviewed ${doc.reviewed_on}` : ""}.
          {!doc.current && (
            <>
              {" "}
              This is not the current version: <a href={href(doc.kind)}>read the current version</a>.
            </>
          )}
        </p>
        {history && history.versions.length > 0 && (
          <nav aria-label="Version history">
            <h2 className="legal-document__history-title">Version history</h2>
            <ul>
              {history.versions.map((v) => (
                <li key={v.version}>
                  <a href={href(doc.kind, v.version)} aria-current={v.version === doc.version ? "page" : undefined}>
                    {v.version}
                  </a>{" "}
                  <span>(effective {v.effective}</span>
                  {v.current && <span>, current</span>}
                  {!v.reviewed && <span>, draft</span>}
                  <span>)</span>
                </li>
              ))}
            </ul>
          </nav>
        )}
      </footer>
    </article>
  );
}
