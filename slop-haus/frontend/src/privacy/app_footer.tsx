import { CookiePreferencesButton, DoNotSellButton } from "@slop-factory/privacy/consent";
import { LegalFooterLinks } from "@slop-factory/privacy/legal_docs";
import { LEGAL_LINKS, legalDocHref } from "./legal_links";
import "./app_footer.scss";

// On every page, signed in or out, so a choice can be changed as easily as
// it was made and the policies are one click from anywhere.
export default function AppFooter() {
  return (
    <footer className="app-footer">
      <LegalFooterLinks href={legalDocHref} links={LEGAL_LINKS}>
        <CookiePreferencesButton />
        <DoNotSellButton />
      </LegalFooterLinks>
    </footer>
  );
}
