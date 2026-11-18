import { useState, type FormEvent } from "react";
import { Button } from "@slop-factory/ui";
import type { EvalSet, EvalSetDefinition } from "@/api";
import {
  EMPTY_DRAFT,
  FILTER_PRIORITIES,
  FILTER_STATUSES,
  definitionFromDraft,
  draftFromSet,
  toggled,
  type EvalSetDraft,
} from "./eval_set_draft";

interface EvalSetFormProps {
  // The set being edited; none for a new one.
  set?: EvalSet;
  busy: boolean;
  // The server's refusal of the last save.
  error: string | null;
  onSave: (definition: EvalSetDefinition) => void;
  onCancel: () => void;
}

// A new set is made from a filter, as the board's are: its tickets are
// whatever matches when the set is used, until "Pin current matches" fixes
// them. A set can also be made from ticket numbers directly.
export default function EvalSetForm({ set, busy, error, onSave, onCancel }: EvalSetFormProps) {
  const [draft, setDraft] = useState<EvalSetDraft>(set ? draftFromSet(set) : EMPTY_DRAFT);
  const [problem, setProblem] = useState<string | null>(null);
  const patch = (next: Partial<EvalSetDraft>) => setDraft((d) => ({ ...d, ...next }));

  function submit(e: FormEvent) {
    e.preventDefault();
    const definition = definitionFromDraft(draft);
    if (typeof definition === "string") {
      setProblem(definition);
      return;
    }
    setProblem(null);
    onSave(definition);
  }

  return (
    <form className="eval-sets__form" onSubmit={submit} aria-label={set ? "Edit eval set" : "New eval set"}>
      <label>
        Name
        <input value={draft.name} maxLength={512} onChange={(e) => patch({ name: e.target.value })} />
      </label>
      <label>
        Description
        <textarea value={draft.description} maxLength={16000} onChange={(e) => patch({ description: e.target.value })} />
      </label>
      {!set && (
        <fieldset>
          <legend>Tickets</legend>
          <label>
            <input type="radio" name="kind" checked={draft.kind === "filter"} onChange={() => patch({ kind: "filter" })} />
            Matching a filter
          </label>
          <label>
            <input type="radio" name="kind" checked={draft.kind === "pinned"} onChange={() => patch({ kind: "pinned" })} />
            Pinned ticket numbers
          </label>
        </fieldset>
      )}
      {draft.kind === "filter" ? (
        <>
          <fieldset>
            <legend>Status</legend>
            {FILTER_STATUSES.map((s) => (
              <label key={s}>
                <input type="checkbox" checked={draft.statuses.includes(s)} onChange={() => patch({ statuses: toggled(draft.statuses, s) })} />
                {s}
              </label>
            ))}
          </fieldset>
          <fieldset>
            <legend>Priority</legend>
            {FILTER_PRIORITIES.map((p) => (
              <label key={p}>
                <input type="checkbox" checked={draft.priorities.includes(p)} onChange={() => patch({ priorities: toggled(draft.priorities, p) })} />
                {p}
              </label>
            ))}
          </fieldset>
          <label>
            Text
            <input value={draft.q} onChange={(e) => patch({ q: e.target.value })} />
          </label>
          <label>
            Closed from
            <input type="date" value={draft.closedFrom} onChange={(e) => patch({ closedFrom: e.target.value })} />
          </label>
          <label>
            Closed to
            <input type="date" value={draft.closedTo} onChange={(e) => patch({ closedTo: e.target.value })} />
          </label>
        </>
      ) : (
        <label>
          Ticket numbers
          <input value={draft.ticketIds} placeholder="#12 #13" onChange={(e) => patch({ ticketIds: e.target.value })} />
        </label>
      )}
      {(problem ?? error) && (
        <p role="alert" className="eval-sets__error">
          {problem ?? error}
        </p>
      )}
      <div className="eval-sets__actions">
        <Button type="submit" variant="primary" disabled={busy}>
          {set ? "Save changes" : "Create set"}
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
