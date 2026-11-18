import { Children, isValidElement, type ReactNode } from "react";
import type { LegalDocHref } from "./types";
import "./legal_footer_links.scss";

export interface LegalLink {
  kind: string;
  label: string;
}

// The built-in kinds, as footer text. An app passes its own list to drop
// the ones it doesn't publish or to add its own kinds.
export const DEFAULT_LEGAL_LINKS: readonly LegalLink[] = [
  { kind: "privacy_policy", label: "Privacy policy" },
  { kind: "cookie_policy", label: "Cookie policy" },
  { kind: "terms", label: "Terms" },
  { kind: "health_data_notice", label: "Health data notice" },
  { kind: "ccpa_notice_at_collection", label: "Notice at collection" },
];

interface LegalFooterLinksProps {
  href: LegalDocHref;
  links?: readonly LegalLink[];
  // Renders after the document links, e.g. a "Cookie settings" button.
  children?: ReactNode;
}

export default function LegalFooterLinks({ href, links = DEFAULT_LEGAL_LINKS, children }: LegalFooterLinksProps) {
  return (
    <nav className="legal-footer-links" aria-label="Legal">
      <ul>
        {links.map((l) => (
          <li key={l.kind}>
            <a href={href(l.kind)}>{l.label}</a>
          </li>
        ))}
        {Children.toArray(children).map((child, i) => (
          // Its own item per child: the ul's gap only separates items.
          <li key={isValidElement(child) && child.key !== null ? child.key : i}>{child}</li>
        ))}
      </ul>
    </nav>
  );
}
