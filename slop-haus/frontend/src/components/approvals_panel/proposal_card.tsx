import { useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type Proposal } from "@/api";
import { errorMessage, useAsyncAction } from "@/lib/use_async_action";
import { isStale, subjectName } from "@/components/proposals";
import ProposalSubject from "./proposal_subject";
import Timestamp from "@/shared/timestamp/timestamp";
import { AttemptEditWarning, editWarning } from "@/components/ticket_detail";
import EditChanges from "./edit_changes";
import ManifestChanges from "./manifest_changes";
import StewardLabel from "./steward_label";
import StewardDraft from "./steward_draft";
import RejectForm from "./reject_form";
import { useUsage } from "@/usage/use_usage";
import "./approvals_panel.scss";

// What the agent's text is, by what approving does with it. The server's
// action line says what happens; this only labels the box beneath it.
function textLabel(p: Proposal): string {
  switch (p.kind) {
    case "discussion_answer":
      return "Comment to be posted as you";
    case "status":
      return "Reason, posted as your comment";
    case "review_decision":
      return "What to address, posted as your comment";
    case "close_merged":
      return "Why the agent thinks it is merged";
    default:
      return "Text from the agent";
  }
}

// Approving these cannot be taken back: a cancelled ticket is closed for
// good and an approved review is merged. They ask once more.
function needsConfirmation(p: Proposal): boolean {
  return (p.kind === "status" && p.status === "cancelled") || (p.kind === "review_decision" && p.decision === "approve");
}

function confirmLabel(p: Proposal): string {
  return p.kind === "status" ? "Confirm: cancel the ticket" : "Confirm: approve for merge";
}

// Past this the text is folded behind "Show all", so a long proposal does
// not push the buttons off a phone's screen.
const FOLD_CHARS = 500;

// AgentText is the proposing agent's own words, as plain text: React escapes
// them, and pre-wrap keeps the author's line breaks. Never markup: the
// person is deciding on what they see here, so it must be what will be
// posted, byte for byte, and not something rendered into something else.
function AgentText({ text, label }: { text: string; label: string }) {
  const [open, setOpen] = useState(false);
  const long = text.length > FOLD_CHARS;
  return (
    <figure className="approvals__text">
      <figcaption>{label}</figcaption>
      <pre className={`approvals__text-body${long && !open ? " approvals__text-body--folded" : ""}`}>{text}</pre>
      {long && (
        <Button variant="ghost" size="sm" className="approvals__more" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? "Show less" : "Show all"}
        </Button>
      )}
    </figure>
  );
}

// What the code host says now. Approving is the confirmation of the warnings:
// the merge is on the base branch either way, so they inform rather than refuse.
function CloseMergedNotes({ check }: { check: NonNullable<Proposal["close_merged"]> }) {
  if (check.problem) {
    return (
      <p role="note" className="approvals__stale">
        Approving would be refused now: {check.problem}
      </p>
    );
  }
  if (check.confirmations.length === 0) return null;
  return (
    <div role="note" className="approvals__stale">
      Approving closes it anyway, though:
      <ul>
        {check.confirmations.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ul>
    </div>
  );
}

export interface CardProps {
  proposal: Proposal;
  onDecided: (message?: string) => void;
}

export default function ProposalCard({ proposal, onDecided }: CardProps) {
  const action = useAsyncAction();
  const [rejecting, setRejecting] = useState(false);
  const [warning, setWarning] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const stale = isStale(proposal);
  const usage = useUsage();

  // A refused decision is reported on the panel, not the card: the server
  // has settled the proposal by then, so the card is about to go.
  const decide = (task: () => Promise<unknown>) =>
    action.run(async () => {
      try {
        await task();
        onDecided();
      } catch (err) {
        onDecided(`Not applied to ${subjectName(proposal)}: ${errorMessage(err, "the request failed")}`);
      }
    }, "Failed.");
  const irreversible = needsConfirmation(proposal);
  const approve = () => {
    usage.event("proposal_decision", { action: "approve" });
    return decide(() => apiClient.approveProposal(proposal.id));
  };
  // An edit to the title or description changes the spec under any attempts
  // running, so the first Approve checks the ticket as it is now and holds the
  // approval back with a warning; Approve anyway confirms. A failed lookup
  // only loses the warning, never the approval.
  async function approveWithWarning() {
    const edit = proposal.edit;
    if (warning === null && edit && proposal.ticket_id !== null && (edit.title || edit.body)) {
      setChecking(true);
      let message: string | null = null;
      try {
        message = editWarning((await apiClient.getTicket(proposal.ticket_id)).attempts);
      } catch {
        // See above.
      } finally {
        setChecking(false);
      }
      if (message) {
        setWarning(message);
        return;
      }
    }
    if (irreversible && !confirming) {
      setConfirming(true);
      return;
    }
    await approve();
  }
  const reject = (reason: string) => {
    usage.event("proposal_decision", { action: "reject" });
    return decide(() => apiClient.rejectProposal(proposal.id, reason));
  };

  return (
    <div className="approvals__card" role="group" aria-label={`Proposal on ${subjectName(proposal)}`}>
      <div className="approvals__head">
        <ProposalSubject proposal={proposal} className="approvals__ticket" idClassName="approvals__id" />
        <span className="approvals__meta">
          from {proposal.agent_name} · expires <Timestamp at={proposal.expires_at} />
        </span>
      </div>

      <p className="approvals__action">{proposal.action_line}</p>

      {proposal.steward && <StewardLabel steward={proposal.steward} />}

      {proposal.draft && <StewardDraft proposalId={proposal.id} draft={proposal.draft} needsConfirmation={irreversible} onApplied={() => onDecided()} />}

      {proposal.edit && proposal.ticket_id !== null && <EditChanges edit={proposal.edit} ticketId={proposal.ticket_id} />}

      {proposal.manifest_edit && <ManifestChanges edit={proposal.manifest_edit} />}

      {proposal.text !== "" && <AgentText text={proposal.text} label={textLabel(proposal)} />}

      {proposal.close_merged && <CloseMergedNotes check={proposal.close_merged} />}

      {warning && <AttemptEditWarning message={warning} />}

      {confirming && (
        <p role="note" className="approvals__stale">
          {proposal.kind === "status" ? "Cancelling closes the ticket for good." : "Approving lets the change be merged."} Press the button again to confirm.
        </p>
      )}

      {stale && (
        <p role="note" className="approvals__stale">
          {proposal.subject === "tenant" ? "The workspace manifest" : "The ticket"} has changed since this was proposed (
          {proposal.stale_reason}), so approving it would be refused.
          Dismiss it and the console can propose again.
        </p>
      )}

      {rejecting ? (
        <RejectForm
          busy={action.busy}
          confirmLabel={stale ? "Dismiss" : "Reject"}
          onConfirm={(reason) => void reject(reason)}
          onCancel={() => setRejecting(false)}
        />
      ) : (
        <div className="approvals__buttons">
          {!stale && (
            <Button variant="primary" disabled={action.busy || checking} onClick={() => void approveWithWarning()}>
              {confirming ? confirmLabel(proposal) : warning ? "Approve anyway" : "Approve"}
            </Button>
          )}
          <Button
            disabled={action.busy}
            onClick={() => (stale ? void reject(`${proposal.subject === "tenant" ? "the manifest" : "the ticket"} changed since it was proposed`) : setRejecting(true))}
          >
            {stale ? "Dismiss" : "Reject"}
          </Button>
        </div>
      )}
    </div>
  );
}
