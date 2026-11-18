import { useState } from "react";
import { Button } from "@slop-factory/ui";
import type { EvalSet, EvalSetDefinition } from "@/api";
import EvalSetForm from "./eval_set_form";
import { describeFilter } from "./eval_set_draft";
import RunVariant from "./run_variant";

interface EvalSetDetailProps {
  set: EvalSet;
  busy: boolean;
  error: string | null;
  onSave: (definition: EvalSetDefinition) => Promise<boolean>;
  onPin: () => void;
  onDelete: () => void;
}

export default function EvalSetDetail({ set, busy, error, onSave, onPin, onDelete }: EvalSetDetailProps) {
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);

  if (editing) {
    return (
      <EvalSetForm
        set={set}
        busy={busy}
        error={error}
        onSave={(definition) => void onSave(definition).then((ok) => ok && setEditing(false))}
        onCancel={() => setEditing(false)}
      />
    );
  }
  const pinned = set.filter === null;
  const ticketIds = set.ticket_ids ?? [];
  return (
    <section className="eval-sets__detail" aria-label={set.name}>
      <h2>{set.name}</h2>
      {set.description && <p>{set.description}</p>}
      {pinned ? (
        <p>
          Pinned: {ticketIds.length} {ticketIds.length === 1 ? "ticket" : "tickets"}
          {ticketIds.length > 0 && ` (${ticketIds.map((id) => `#${id}`).join(" ")})`}
        </p>
      ) : (
        <p>Matches a filter: {describeFilter(set.filter!)}. The tickets are found each time the set is used.</p>
      )}
      {error && <p role="alert" className="eval-sets__error">{error}</p>}
      <div className="eval-sets__actions">
        <Button type="button" variant="ghost" onClick={() => setEditing(true)}>
          Edit
        </Button>
        {!pinned && (
          <Button type="button" variant="ghost" disabled={busy} onClick={onPin}>
            Pin current matches
          </Button>
        )}
        {deleting ? (
          <>
            <Button type="button" variant="danger" disabled={busy} onClick={onDelete}>
              Delete “{set.name}” and its history
            </Button>
            <Button type="button" variant="ghost" onClick={() => setDeleting(false)}>
              Keep
            </Button>
          </>
        ) : (
          <Button type="button" variant="ghost" onClick={() => setDeleting(true)}>
            Delete
          </Button>
        )}
      </div>
      {pinned ? (
        <RunVariant set={set} />
      ) : (
        <p className="eval-sets__hint">
          Pin the current matches to run a variant across them: the number of replays is confirmed against a fixed list
          of tickets.
        </p>
      )}
    </section>
  );
}
