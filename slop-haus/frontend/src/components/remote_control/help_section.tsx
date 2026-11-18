import type { ReactNode } from "react";
import "./help_section.scss";

interface HelpSectionProps {
  className: string;
  title: string;
  // Desktop opens the section as a view of the main pane, with a heading; the
  // phone keeps it as a closed section under the list of sessions.
  asView: boolean;
  children: ReactNode;
}

export default function HelpSection({ className, title, asView, children }: HelpSectionProps) {
  if (asView) {
    return (
      <section className={`${className} help-section--view`} aria-label={title}>
        <h2 className="help-section__title">{title}</h2>
        {children}
      </section>
    );
  }
  return (
    <details className={className}>
      <summary>{title}</summary>
      {children}
    </details>
  );
}
