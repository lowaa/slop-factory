import { useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type Proposal } from "@/api";
import { useAsyncAction } from "@/lib/use_async_action";
import { useUsage } from "@/usage/use_usage";
import "./steward_draft.scss";

type Draft = NonNullable<Proposal["draft"]>;

export interface StewardDraftProps {
  proposalId: string;
  draft: Draft;
  // Irreversible approvals (a cancellation, a merge) ask once more, as the
  // card's own Approve does.
  needsConfirmation: boolean;
  onApplied: () => void;
}

// StewardDraft is the steward's pre-drafted decision (assist mode). Every
// check is listed, passing or failing, so the owner sees what the draft rests
// on. The text is the steward's own words: plain text, as with agent text.
// A refusal (stale, already applied, notes only) comes from the server, which
// checks the draft again on apply, and is shown here without hiding the card.
export default function StewardDraft({ proposalId, draft, needsConfirmation, onApplied }: StewardDraftProps) {
  const action = useAsyncAction();
  const usage = useUsage();
  const [confirming, setConfirming] = useState(false);
  const apply = () => {
    if (needsConfirmation && !confirming) {
      setConfirming(true);
      return;
    }
    usage.event("proposal_decision", { action: "apply_steward_draft" });
    return action.run(async () => {
      await apiClient.applyStewardDraft(proposalId);
      onApplied();
    }, "Failed to apply the draft.");
  };

  return (
    <section className="steward-draft" aria-label="Steward's draft">
      <h4 className="steward-draft__title">
        Steward&apos;s draft <span className="steward-draft__category">{draft.category}</span>
        {draft.would_send_back && <span className="steward-draft__badge">would send back</span>}
      </h4>
      {draft.reason !== "" && <p className="steward-draft__reason">{draft.reason}</p>}
      <ul className="steward-draft__evidence">
        {draft.evidence.map((e, i) => (
          <li key={`${e.check}-${i}`} className={e.ok ? "steward-draft__ok" : "steward-draft__failed"}>
            {e.ok ? "✓" : "✗"} {e.check}
            {e.detail ? `: ${e.detail}` : ""}
            {e.ref ? ` (Ref: ${e.ref})` : ""}
          </li>
        ))}
      </ul>
      {draft.body !== "" && (
        <figure className="steward-draft__text">
          <figcaption>{draft.would_send_back ? "Review notes" : "Draft text"}</figcaption>
          <pre>{draft.body}</pre>
        </figure>
      )}
      {draft.applied ? (
        <p className="steward-draft__note">This draft was already applied.</p>
      ) : (
        !draft.would_send_back && (
          <Button variant="primary" disabled={action.busy} onClick={() => void apply()}>
            {confirming ? "Confirm: apply steward's draft" : "Apply steward's draft"}
          </Button>
        )
      )}
      {action.error && (
        <p role="alert" className="steward-draft__error">
          {action.error}
        </p>
      )}
    </section>
  );
}
