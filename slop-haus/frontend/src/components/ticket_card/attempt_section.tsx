import type { AttemptCard } from "@/api";
import { WorkerCost } from "@/components/worker_cost";

// What is particular to an attempt's card: where it stands among the
// ticket's attempts, the variant it runs, and what its worker has cost.
export default function AttemptSection({ attempt }: { attempt: AttemptCard }) {
  const variant = attempt.variant;
  return (
    <div className="ticket-card__attempt">
      <span>
        {attempt.number} of {attempt.total} attempts
      </span>
      {variant && (
        <span className="ticket-card__variant">
          {variant.name} v{variant.version}
        </span>
      )}
      <WorkerCost figures={attempt.worker_cost} className="ticket-card__cost" />
      {attempt.awaiting_choice && (
        <span className="ticket-card__badge ticket-card__badge--waiting">awaiting choice</span>
      )}
    </div>
  );
}
