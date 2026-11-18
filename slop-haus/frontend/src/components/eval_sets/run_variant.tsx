import { useEffect, useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type EvalSet, type VariantOption } from "@/api";
import { ReplayBatchPanel, useReplayBatch } from "@/components/replay_batch";
import EvalSetHistory from "./eval_set_history";

// The replay panel over a pinned set's tickets, and the set's history. The
// panel's count, cap and confirmation are the batch panel's own; only the
// endpoint differs.
export default function RunVariant({ set }: { set: EvalSet }) {
  const [open, setOpen] = useState(false);
  const [variants, setVariants] = useState<VariantOption[]>([]);
  useEffect(() => {
    let cancelled = false;
    apiClient
      .listVariantOptions()
      .then((loaded) => {
        if (!cancelled) setVariants(loaded);
      })
      .catch(() => {
        // Without the list the default variant can still be replayed.
      });
    return () => {
      cancelled = true;
    };
  }, []);
  const ticketIds = set.ticket_ids ?? [];
  const batch = useReplayBatch(ticketIds, (versionIds, runs, confirm) =>
    apiClient.runEvalSet(set.id, versionIds, runs, confirm),
  );

  return (
    <>
      {!batch.unavailable &&
        (open ? (
          <ReplayBatchPanel ticketCount={ticketIds.length} variants={variants} batch={batch} />
        ) : (
          <Button type="button" variant="primary" onClick={() => setOpen(true)}>
            Run variant…
          </Button>
        ))}
      <EvalSetHistory setId={set.id} variants={variants} refreshKey={batch.result?.batch_id} />
    </>
  );
}
