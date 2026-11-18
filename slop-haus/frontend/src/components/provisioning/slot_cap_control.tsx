import { useState, type FormEvent } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type ProvisionSupervisor } from "@/api";
import { MAX_SLOT_CAP, parseSlotCap } from "@/domain/slot_cap";
import { useAsyncAction } from "@/lib/use_async_action";

interface Props {
  supervisor: ProvisionSupervisor;
  // Only the tenant's owner may change the cap; the server refuses anyone else.
  canEdit: boolean;
  onChanged: () => Promise<void>;
}

// How many workers a supervisor has of the number it may have, with the owner's
// way to change that. Lowering the cap below the count retires nothing: the
// supervisor just cannot create more until enough workers go.
export default function SlotCapControl({ supervisor, canEdit, onChanged }: Props) {
  const action = useAsyncAction();
  const [draft, setDraft] = useState<string | null>(null);
  const parsed = draft === null ? null : parseSlotCap(draft);
  const overCap = supervisor.slot_count > supervisor.slot_cap;

  async function save(e: FormEvent) {
    e.preventDefault();
    if (parsed === null) return;
    await action.run(async () => {
      await apiClient.setSupervisorSlotCap(supervisor.id, parsed);
      await onChanged();
      setDraft(null);
    }, "Could not change the worker cap.");
  }

  return (
    <div className="provisioning__slot-cap">
      <span>
        {supervisor.slot_count} of {supervisor.slot_cap} workers
      </span>
      {overCap && (
        <p role="note">
          This supervisor has more workers than its cap, so it cannot create more until some retire or the cap is raised.
        </p>
      )}
      {action.error !== null && <p role="alert">{action.error}</p>}
      {canEdit && draft === null && (
        <Button size="sm" onClick={() => setDraft(String(supervisor.slot_cap))}>
          Change cap for {supervisor.name}
        </Button>
      )}
      {canEdit && draft !== null && (
        <form onSubmit={save}>
          <label>
            Workers {supervisor.name} may run at once
            <input
              type="number"
              min={1}
              max={MAX_SLOT_CAP}
              step={1}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              aria-invalid={parsed === null}
            />
          </label>
          {parsed === null && <p role="note">Enter a whole number from 1 to {MAX_SLOT_CAP}.</p>}
          <Button type="submit" size="sm" disabled={action.busy || parsed === null || parsed === supervisor.slot_cap}>
            Save cap
          </Button>
          <Button size="sm" disabled={action.busy} onClick={() => setDraft(null)}>
            Cancel
          </Button>
        </form>
      )}
    </div>
  );
}
