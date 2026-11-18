import { useId, type ReactNode } from "react";
import "./settings_section.scss";

interface SettingsSectionProps {
  title: string;
  // One line on what the group decides, so the grouping explains itself.
  summary: string;
  // Sections sit under different headings -- the new source form's h2, a
  // source card's h4 -- and a heading level may only step down by one.
  level: 3 | 5;
  children: ReactNode;
}

export default function SettingsSection({
  title,
  summary,
  level,
  children,
}: SettingsSectionProps) {
  const headingId = useId();
  const Heading = `h${level}` as const;
  return (
    <div className="settings-section" role="group" aria-labelledby={headingId}>
      <Heading id={headingId}>{title}</Heading>
      <p className="settings-section__summary">{summary}</p>
      <div className="settings-section__body">{children}</div>
    </div>
  );
}
