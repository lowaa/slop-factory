import { LegalPage as SharedLegalPage } from "@slop-factory/privacy/legal_docs";
import type { LegalDocsApi } from "@slop-factory/privacy/legal_docs";
import { legalDocHref } from "./legal_links";
import "./legal_page.scss";

interface LegalPageProps {
  kind: string;
  version?: string;
  api?: LegalDocsApi;
}

const BACK_LINK = { href: "/", label: "Back to slop-haus" };

export default function LegalPage(props: LegalPageProps) {
  return <SharedLegalPage {...props} href={legalDocHref} className="legal-page" backLink={BACK_LINK} />;
}
