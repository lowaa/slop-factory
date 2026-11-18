import { Button } from "@slop-factory/ui";
import "./fleet_instructions.scss";

export interface VersionDraft {
  name: string;
  body: string;
  makeDefault: boolean;
}

const byteLength = (s: string) => new TextEncoder().encode(s.trim()).length;

// VersionEditor is the form for the next version of a name: it never edits a
// saved one.
export default function VersionEditor({
  draft,
  maxBodyBytes,
  busy,
  onChange,
  onSave,
  fieldLabel = "Instructions",
  allowEmpty = false,
}: {
  draft: VersionDraft;
  maxBodyBytes: number;
  busy: boolean;
  onChange: (draft: VersionDraft) => void;
  onSave: () => void;
  fieldLabel?: string;
  allowEmpty?: boolean;
}) {
  const size = byteLength(draft.body);
  const tooBig = size > maxBodyBytes;
  return (
    <form
      className="fleet-instructions__form"
      onSubmit={(e) => {
        e.preventDefault();
        onSave();
      }}
    >
      <label className="fleet-instructions__field">
        Name
        <input value={draft.name} onChange={(e) => onChange({ ...draft, name: e.target.value })} maxLength={64} />
      </label>
      <label className="fleet-instructions__field">
        {fieldLabel}
        <textarea value={draft.body} onChange={(e) => onChange({ ...draft, body: e.target.value })} />
      </label>
      <p className={tooBig ? "fleet-instructions__error" : "fleet-instructions__hint"}>
        {size} / {maxBodyBytes} bytes
      </p>
      <label className="fleet-instructions__checkbox">
        <input
          type="checkbox"
          checked={draft.makeDefault}
          onChange={(e) => onChange({ ...draft, makeDefault: e.target.checked })}
        />{" "}
        Make this version the default
      </label>
      <div className="fleet-instructions__actions">
        <Button
          type="submit"
          variant="primary"
          disabled={busy || tooBig || draft.name.trim() === "" || (!allowEmpty && draft.body.trim() === "")}
        >
          Save as a new version
        </Button>
      </div>
    </form>
  );
}
