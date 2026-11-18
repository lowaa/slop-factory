import type { RefObject } from "react";

interface ValidationInputProps {
  // The resolved selection, not the raw value prop: a value the options
  // no longer contain shows the placeholder, so it must also read as
  // empty here or the form would submit a selection the user can't see.
  value: string | null;
  required: boolean;
  // Where focus goes when the browser focuses this input to report it
  // invalid, so the user lands on the control they actually need to use.
  focusTarget: RefObject<HTMLElement | null>;
}

// Neither combobox is a form control the browser knows about, so `required`
// on its own would do nothing. This hidden input carries the value into
// native constraint validation instead: submit is blocked and the browser's
// own message is shown, same as the <select required> these replaced.
// Deliberately not readOnly -- read-only inputs are barred from constraint
// validation -- and not display:none, which makes the browser refuse to
// submit with an "invalid form control is not focusable" console error.
export default function ValidationInput({ value, required, focusTarget }: ValidationInputProps) {
  if (!required) return null;
  return (
    <input
      className="dropdown__validation"
      tabIndex={-1}
      aria-hidden="true"
      required
      value={value ?? ""}
      onChange={() => {}}
      onFocus={() => focusTarget.current?.focus()}
    />
  );
}
