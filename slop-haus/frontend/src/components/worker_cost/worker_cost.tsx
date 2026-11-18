import type { AttemptCard } from "@/api";
import { formatCost } from "@/lib/format_usage";
import { costKind } from "@/domain/llm_usage_labels";

// One figure for each way the usage was billed, each with its kind -- spent
// or estimated -- so the two are never added up as one sum of money. Usage
// no price could be found for reads "unpriced", never $0.00.
export default function WorkerCost({
  figures,
  className,
}: {
  figures: AttemptCard["worker_cost"];
  className?: string;
}) {
  if (figures.length === 0) return null;
  return (
    <span className={className}>
      {figures.map((f) => (
        <span key={f.billing_mode}>
          {f.cost_micros === null
            ? "unpriced"
            : `${formatCost(f.cost_micros)} ${costKind(f.billing_mode)}`}
          {f.cost_micros !== null && f.unpriced_records > 0 && " + unpriced"}
        </span>
      ))}
    </span>
  );
}
