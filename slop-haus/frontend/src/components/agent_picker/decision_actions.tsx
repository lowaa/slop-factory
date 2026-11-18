import { Button } from "@slop-factory/ui";
import "./decision_actions.scss";

export type Decision = "approve" | "deny";

interface DecisionActionsProps {
  canApprove: boolean;
  submitting: Decision | null;
  onDecide: (decision: Decision) => void;
}

// Approve is the one primary action on these pages, so it gets the filled
// accent like every other auth screen's submit; Deny is an ordinary way
// out, not a destructive one, so it stays secondary.
export function DecisionActions({ canApprove, submitting, onDecide }: DecisionActionsProps) {
  return (
    <div className="decision-actions">
      <Button
        variant="primary"
        className="decision-actions__approve"
        disabled={!canApprove || submitting !== null}
        onClick={() => onDecide("approve")}
      >
        {submitting === "approve" ? "Approving…" : "Approve"}
      </Button>
      <Button variant="secondary" disabled={submitting !== null} onClick={() => onDecide("deny")}>
        {submitting === "deny" ? "Denying…" : "Deny"}
      </Button>
    </div>
  );
}
