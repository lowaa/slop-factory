import type { LlmUsageSums } from "@/api";
import { formatCost } from "@/lib/format_usage";
import { costKind } from "@/domain/llm_usage_labels";

interface CostProps {
  sums: Pick<LlmUsageSums, "billing_mode" | "cost_micros" | "unpriced_records" | "unpriced_requests">;
  // Show what the cost leaves out; off where a row is too small for it.
  detail?: boolean;
}

// Cost is a figure with its kind, or "unpriced" -- never $0.00 for usage
// nothing could price.
export default function Cost({ sums, detail = false }: CostProps) {
  const { cost_micros: cost, billing_mode: mode } = sums;
  const unpriced = sums.unpriced_records > 0;
  return (
    <span className="llm-usage__cost" data-mode={mode}>
      {cost === null ? (
        <span className="llm-usage__unpriced">unpriced</span>
      ) : (
        <>
          <span className="llm-usage__amount">{formatCost(cost)}</span>{" "}
          <span className="llm-usage__kind">{costKind(mode)}</span>
          {unpriced && !detail && <span className="llm-usage__unpriced"> + unpriced</span>}
        </>
      )}
      {detail && unpriced && (
        <span className="llm-usage__note">
          {cost === null ? "" : " "}({sums.unpriced_records} {sums.unpriced_records === 1 ? "record" : "records"},{" "}
          {sums.unpriced_requests} requests, with no price: not in the cost)
        </span>
      )}
    </span>
  );
}
