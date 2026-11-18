import type { ReactNode } from "react";
import "./pr_link.scss";

interface PrLinkProps {
  url: string | null;
  // children is the link text; the URL itself when omitted.
  children?: ReactNode;
  // ariaLabel names the link for a screen reader when the visible text
  // alone would not say which ticket it belongs to.
  ariaLabel?: string;
  className?: string;
}

// The server only stores https links, but this is the one place an agent's
// report becomes an href, so it checks again rather than trusting that
// every row ever written went through that validation.
function safePrUrl(url: string | null): string | null {
  if (!url) return null;
  try {
    return new URL(url).protocol === "https:" ? url : null;
  } catch {
    return null;
  }
}

// A link to the reviewable change a ticket was merged through. Renders
// nothing without one.
export default function PrLink({
  url,
  children,
  ariaLabel,
  className,
}: PrLinkProps) {
  const href = safePrUrl(url);
  if (!href) return null;
  return (
    <a
      className={["pr-link", className].filter(Boolean).join(" ")}
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={ariaLabel}
      // The link is an agent's report, not something slop-haus checked, so
      // where it goes is shown, not just "PR".
      title={href}
    >
      {children ?? href}
    </a>
  );
}

// commitUrl is the link of a commit in the repository a PR link points into,
// or null when the PR link is not of the form <repo>/pull/<n>. The server
// stores no commit links of its own, so this follows the PR link's host.
export function commitUrl(prUrl: string | null, sha: string): string | null {
  const href = safePrUrl(prUrl);
  if (!href || !/^[0-9a-f]+$/.test(sha)) return null;
  const match = /^(https:\/\/[^?#]*?)\/pull\/\d+\/?$/.exec(href);
  return match ? `${match[1]}/commit/${sha}` : null;
}
