import { Dropdown, type DropdownOption } from "@slop-factory/ui";
import IntakeField from "./intake_field";
import { FIELD_DOCS, type FieldKey } from "./intake_field_docs";

interface IntakeSelectProps<T extends string> {
  field: FieldKey;
  value: T;
  options: DropdownOption<T>[];
  onChange: (value: T) => void;
  hint?: string;
  disabled?: boolean;
}

// An intake field whose control is a Dropdown. The field's visible label is
// drawn by IntakeField; the Dropdown carries the same text as its accessible
// name, hidden so it is not shown twice.
export default function IntakeSelect<T extends string>({
  field,
  value,
  options,
  onChange,
  hint,
  disabled,
}: IntakeSelectProps<T>) {
  const doc = FIELD_DOCS[field];
  return (
    <IntakeField field={field} hint={hint} asText>
      {() => (
        <Dropdown
          label={doc.label}
          labelHidden
          value={value}
          options={options}
          onChange={onChange}
          disabled={disabled}
        />
      )}
    </IntakeField>
  );
}
