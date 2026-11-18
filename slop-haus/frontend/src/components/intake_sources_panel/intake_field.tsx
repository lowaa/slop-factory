import { useId, type ReactElement } from "react";
import { Tooltip } from "@slop-factory/ui";
import { FIELD_DOCS, type FieldKey } from "./intake_field_docs";
import "./intake_field.scss";

interface IntakeFieldProps {
  field: FieldKey;
  // Replaces the doc's hint, for a state that matters more than it (a
  // control that is disabled and why).
  hint?: string;
  // The control brings its own accessible name (a Dropdown is a button, so
  // a <label for> cannot name it); the visible label is then plain text.
  asText?: boolean;
  // The control, given the id its label points at. The tooltip wraps it
  // rather than the label so the bubble's words are not part of the
  // label's accessible name.
  children: (id: string) => ReactElement<{ "aria-describedby"?: string }>;
}

export default function IntakeField({
  field,
  hint,
  asText = false,
  children,
}: IntakeFieldProps) {
  const id = useId();
  const doc = FIELD_DOCS[field];
  return (
    <div className="intake-field">
      {asText ? (
        <span className="intake-field__label" aria-hidden="true">
          {doc.label}
        </span>
      ) : (
        <label htmlFor={id}>{doc.label}</label>
      )}
      <Tooltip text={doc.tooltip} hoverDelayMs={0}>
        {children(id)}
      </Tooltip>
      <span className="intake-field__hint">{hint ?? doc.hint}</span>
    </div>
  );
}
