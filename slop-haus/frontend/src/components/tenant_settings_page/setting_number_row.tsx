import { useId, useState } from "react";
import { Button } from "@slop-factory/ui";
import "./setting_number_row.scss";
import { errorMessage } from "@/lib/use_async_action";

interface SettingNumberRowProps {
  label: string;
  description?: string;
  value: number;
  min: number;
  max: number;
  // What the number counts; omitted for a bare count.
  unit?: string;
  // Shown as the value with no field, for someone who may not change it.
  readOnlyNote?: string;
  // Resolves once the new value is stored; a rejection is shown against the
  // row and the field keeps what the server has.
  onWrite: (next: number) => Promise<void>;
  children?: React.ReactNode;
}

export default function SettingNumberRow({
  label,
  description,
  value,
  min,
  max,
  unit,
  readOnlyNote,
  onWrite,
  children,
}: SettingNumberRowProps) {
  const [draft, setDraft] = useState(String(value));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const descriptionId = useId();
  const next = Number(draft);
  const valid =
    draft.trim() !== "" && Number.isInteger(next) && next >= min && next <= max;

  const range = `${min}–${max}`;

  async function save() {
    if (saving || !valid) return;
    setSaving(true);
    setError(null);
    try {
      await onWrite(next);
    } catch (err: unknown) {
      setDraft(String(value));
      setError(
        errorMessage(err, `Failed to change ${label}.`),
      );
    } finally {
      setSaving(false);
    }
  }

  if (readOnlyNote !== undefined) {
    return (
      <div className="setting-number-row">
        <span className="setting-number-row__label">{label}</span>
        {description && (
          <p className="setting-number-row__description">{description}</p>
        )}
        <p className="tenant-settings__value">
          {value}
          {unit ? ` ${unit}` : ""}
        </p>
        <p className="tenant-settings__meta">{readOnlyNote}</p>
        {children}
      </div>
    );
  }

  return (
    <div className="setting-number-row">
      <span className="setting-number-row__label" aria-hidden="true">
        {label}
      </span>
      {description && (
        <p id={descriptionId} className="setting-number-row__description">
          {description}
        </p>
      )}
      <div className="setting-number-row__field">
        <input
          type="number"
          aria-label={label}
          aria-describedby={description ? descriptionId : undefined}
          min={min}
          max={max}
          value={draft}
          disabled={saving}
          onChange={(e) => setDraft(e.target.value)}
        />
        <span>{unit ? `${unit} (${range})` : `(${range})`}</span>
        <Button
          onClick={() => void save()}
          disabled={saving || !valid || next === value}
        >
          Save
        </Button>
      </div>
      {children}
      {!valid && (
        <p role="alert" className="setting-number-row__error">
          Enter a whole number from {min} to {max}.
        </p>
      )}
      {error && (
        <p role="alert" className="setting-number-row__error">
          {error}
        </p>
      )}
    </div>
  );
}
