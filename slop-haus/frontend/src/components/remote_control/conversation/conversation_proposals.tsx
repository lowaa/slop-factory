import { useState } from "react";
import { Link } from "@slop-factory/router";
import type { Proposal } from "@/api";
import { isStale, useProposals } from "@/components/proposals";
import { ProposalCard } from "@/components/approvals_panel";

// ConversationProposals shows what the remote control has proposed that is still
// waiting, in the chat where it was asked for. Collapsed, a proposal is an
// action line and a short preview, so a long one cannot crowd the chat out of
// a small screen. Opened, it is the Approvals tab's own card, with its stale
// check, change views and confirmations, so deciding here is no weaker than
// there. The text is the agent's own, shown as plain text.
const PREVIEW_LINES = 2;
const PREVIEW_CHARS = 160;

function previewOf(text: string): { preview: string; truncated: boolean } {
  const lines = text.split("\n").filter((l) => l.trim() !== "");
  let preview = lines.slice(0, PREVIEW_LINES).join("\n");
  let truncated = lines.length > PREVIEW_LINES;
  if (preview.length > PREVIEW_CHARS) {
    preview = preview.slice(0, PREVIEW_CHARS);
    truncated = true;
  }
  return { preview: truncated ? `${preview.trimEnd()}…` : preview, truncated };
}

function ProposalItem({ proposal, onDecided }: { proposal: Proposal; onDecided: (message?: string) => void }) {
  const [open, setOpen] = useState(false);
  const { preview, truncated } = previewOf(proposal.text);
  // Deciding is only offered in the expanded proposal, so it is made on the
  // whole text and the same card the Approvals tab shows.
  return (
    <li className="conversation__proposal">
      <p className="conversation__proposal-action">
        <strong>Proposed:</strong> {proposal.action_line}
        {isStale(proposal) && " (the ticket has changed since: it cannot be approved)"}
      </p>
      {open ? (
        <div className="conversation__proposal-card">
          <ProposalCard proposal={proposal} onDecided={onDecided} />
        </div>
      ) : (
        proposal.text !== "" && <pre className="conversation__proposal-text">{truncated ? preview : proposal.text}</pre>
      )}
      <button
        type="button"
        className="conversation__proposal-toggle"
        aria-expanded={open}
        aria-label={`${open ? "Hide" : "Show"} details of: ${proposal.action_line}`}
        onClick={() => setOpen(!open)}
      >
        {open ? "Hide details" : "Show details"}
      </button>
      <Link to="/approvals" className="conversation__proposal-link">
        Review and approve on the Approvals tab
      </Link>
    </li>
  );
}

export default function ConversationProposals({ proposals }: { proposals: Proposal[] }) {
  const { reload } = useProposals();
  const [notice, setNotice] = useState<string | null>(null);
  const decided = (message?: string) => {
    setNotice(message ?? null);
    void reload();
  };
  if (proposals.length === 0 && notice === null) return null;
  return (
    <>
      {notice && (
        <p role="status" className="conversation__proposal-notice">
          {notice}
        </p>
      )}
      <ul className="conversation__proposals" aria-label="Proposed by this remote control">
        {proposals.map((p) => (
          <ProposalItem key={p.id} proposal={p} onDecided={decided} />
        ))}
      </ul>
    </>
  );
}
