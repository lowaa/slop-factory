import { useState } from "react";
import { Button } from "@slop-factory/ui";

export interface RejectFormProps {
  busy: boolean;
  confirmLabel: string;
  onConfirm: (reason: string) => void;
  onCancel: () => void;
}

export default function RejectForm({ busy, confirmLabel, onConfirm, onCancel }: RejectFormProps) {
  const [reason, setReason] = useState("");
  return (
    <form
      className="approvals__reject"
      onSubmit={(e) => {
        e.preventDefault();
        onConfirm(reason);
      }}
    >
      <label>
        Reason (the console reads it back)
        <textarea value={reason} maxLength={1000} rows={2} onChange={(e) => setReason(e.target.value)} />
      </label>
      <div className="approvals__buttons">
        <Button type="submit" variant="danger" disabled={busy}>
          {confirmLabel}
        </Button>
        <Button onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
