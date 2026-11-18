import { useState } from "react";
import { Button } from "@slop-factory/ui";
import type { EvalSetDefinition } from "@/api";
import EvalSetDetail from "./eval_set_detail";
import EvalSetForm from "./eval_set_form";
import { describeFilter } from "./eval_set_draft";
import { useEvalSets } from "./use_eval_sets";
import "./eval_sets.scss";

export default function EvalSetsPage() {
  const model = useEvalSets();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const { state } = model;

  const select = (id: string | null) => {
    model.clearError();
    setCreating(false);
    setSelectedId(id);
  };
  async function create(definition: EvalSetDefinition) {
    const made = await model.create(definition);
    if (made) {
      setCreating(false);
      setSelectedId(made.id);
    }
  }

  const selected = state.status === "loaded" ? state.sets.find((s) => s.id === selectedId) : undefined;
  return (
    <div className="eval-sets">
      <h1>Eval sets</h1>
      <p className="eval-sets__hint">
        A set is a group of done tickets, saved as a filter or pinned as a list, that variants are replayed across.
      </p>
      {state.status === "loading" && <p>Loading…</p>}
      {state.status === "error" && <p role="alert" className="eval-sets__error">{state.message}</p>}
      {state.status === "loaded" && (
        <>
          {state.sets.length === 0 && <p className="eval-sets__hint">No eval sets yet.</p>}
          <ul className="eval-sets__list">
            {state.sets.map((s) => (
              <li key={s.id}>
                <Button type="button" variant="ghost" aria-pressed={s.id === selectedId} onClick={() => select(s.id)}>
                  {s.name}
                </Button>{" "}
                <span className="eval-sets__hint">
                  {s.filter ? describeFilter(s.filter) : `${(s.ticket_ids ?? []).length} pinned`}
                </span>
              </li>
            ))}
          </ul>
          {!creating && (
            <Button type="button" variant="primary" onClick={() => (select(null), setCreating(true))}>
              New eval set
            </Button>
          )}
        </>
      )}
      {creating && (
        <EvalSetForm busy={model.busy} error={model.error} onSave={(d) => void create(d)} onCancel={() => setCreating(false)} />
      )}
      {selected && !creating && (
        <EvalSetDetail
          key={selected.id}
          set={selected}
          busy={model.busy}
          error={model.error}
          onSave={async (d) => (await model.replace(selected.id, d)) !== null}
          onPin={() => void model.pin(selected.id)}
          onDelete={() => void model.remove(selected.id).then((ok) => ok && setSelectedId(null))}
        />
      )}
    </div>
  );
}
