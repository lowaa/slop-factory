import type { SignUpExtras } from "../config";

// A stand-in for an app's agreement inputs: one checkbox, which must be
// ticked for the sign-up to go ahead.
export const agreeExtras: SignUpExtras = {
  Fields: ({ values, onChange, disabled }) => (
    <label>
      <input
        type="checkbox"
        disabled={disabled}
        checked={values.agreed === true}
        onChange={(e) => onChange({ ...values, agreed: e.target.checked })}
      />
      I agree
    </label>
  ),
  isComplete: (values) => values.agreed === true,
};
