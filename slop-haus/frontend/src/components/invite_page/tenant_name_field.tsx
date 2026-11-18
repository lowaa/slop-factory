import { MAX_TENANT_NAME_LENGTH } from "@/domain/tenant_names";

interface TenantNameFieldProps {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  hint?: string;
}

// What the workspace an invite makes is called. It is how the person tells
// it from their other workspaces, so it is asked for rather than assumed.
export default function TenantNameField({
  value,
  onChange,
  disabled,
  hint,
}: TenantNameFieldProps) {
  return (
    <>
      <label className="auth-layout__field">
        <span>Workspace name</span>
        <input
          type="text"
          required
          maxLength={MAX_TENANT_NAME_LENGTH}
          value={value}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
        />
      </label>
      {hint && <small>{hint}</small>}
    </>
  );
}
