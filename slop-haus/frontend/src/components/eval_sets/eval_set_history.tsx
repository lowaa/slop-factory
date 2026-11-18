import { useEffect, useState } from "react";
import { apiClient, type EvalSetBatch, type VariantOption } from "@/api";
import { errorMessage } from "@/lib/use_async_action";

interface EvalSetHistoryProps {
  setId: string;
  variants: VariantOption[];
  // Changes when a batch has just been added, to load the history again.
  refreshKey?: string;
}

function variantLabel(variants: VariantOption[], id: string | null): string {
  if (id === null) return "Default";
  const v = variants.find((o) => o.id === id);
  return v ? `${v.name} v${v.version}` : "an archived variant";
}

// The batches run across a set, newest first: when, under which variants,
// and over how many tickets.
export default function EvalSetHistory({ setId, variants, refreshKey }: EvalSetHistoryProps) {
  const [batches, setBatches] = useState<EvalSetBatch[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    apiClient
      .listEvalSetBatches(setId)
      .then((loaded) => {
        if (cancelled) return;
        setError(null);
        setBatches(loaded);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(errorMessage(err, "Failed to load the batch history."));
      });
    return () => {
      cancelled = true;
    };
  }, [setId, refreshKey]);

  return (
    <section className="eval-sets__history" aria-label="Batch history">
      <h3>Batches run</h3>
      {error && <p role="alert" className="eval-sets__error">{error}</p>}
      {!error && batches === null && <p>Loading…</p>}
      {batches?.length === 0 && <p className="eval-sets__hint">No batch has been run across this set.</p>}
      {batches && batches.length > 0 && (
        <ul>
          {batches.map((b) => (
            <li key={b.batch_id}>
              <time dateTime={b.created_at}>{new Date(b.created_at).toLocaleString()}</time>:{" "}
              {b.version_ids.map((v) => variantLabel(variants, v)).join(", ")} — {b.runs} {b.runs === 1 ? "run" : "runs"} on{" "}
              {b.ticket_count} {b.ticket_count === 1 ? "ticket" : "tickets"}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
