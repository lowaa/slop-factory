import { useState } from "react";
import { Dropdown } from "@slop-factory/ui";
import type { DecidedProposalState, Proposal, ProposalTicketFilter } from "@/api";
import { AuthorName } from "@/components/author_name";
import ProposalSubject from "./proposal_subject";
import { subjectName } from "@/components/proposals";
import Timestamp from "@/shared/timestamp/timestamp";
import VirtualList from "@/shared/virtual_list/virtual_list";
import { isAutoApproved } from "@/components/proposals";
import EditChanges from "./edit_changes";
import { useDecidedProposals } from "./use_decided_proposals";
import "./decided_proposals.scss";

const STATE_OPTIONS: { value: DecidedProposalState; label: string }[] = [
  { value: "approved", label: "Approved" },
  { value: "rejected", label: "Rejected" },
  { value: "withdrawn", label: "Withdrawn" },
  { value: "expired", label: "Expired" },
  { value: "voided", label: "Voided" },
  { value: "stale", label: "Stale" },
  { value: "failed", label: "Failed" },
];

const STATE_LABEL = new Map(STATE_OPTIONS.map((o) => [o.value, o.label]));

const CARD_HEIGHT = 160;

// Read-only: what happened to a proposal is settled, so there is nothing to
// press. The agent's text is shown as plain text, as on a pending card.
export function DecidedCard({ proposal }: { proposal: Proposal }) {
  const auto = isAutoApproved(proposal);
  return (
    <div className="decided__card" role="group" aria-label={`Decided proposal on ${subjectName(proposal)}`}>
      <div className="decided__head">
        <ProposalSubject proposal={proposal} className="decided__ticket" idClassName="decided__id" />
        <span className="decided__meta">from {proposal.agent_name}</span>
      </div>

      <p className="decided__action">{proposal.action_line}</p>
      {proposal.edit && proposal.ticket_id !== null && <EditChanges edit={proposal.edit} ticketId={proposal.ticket_id} />}
      {proposal.text !== "" && <pre className="decided__text">{proposal.text}</pre>}

      <p className="decided__outcome">
        <span className={`decided__state decided__state--${proposal.state}`}>{STATE_LABEL.get(proposal.state as DecidedProposalState) ?? proposal.state}</span>
        {auto && <span className="decided__auto">auto-approved</span>}
        {proposal.decided_at && <Timestamp at={proposal.decided_at} />}
        {proposal.decided_by_user_id !== null || proposal.decided_by_user_id_deleted ? (
          <span>
            by{" "}
            <AuthorName agentId={null} userId={proposal.decided_by_user_id} userDeleted={proposal.decided_by_user_id_deleted ?? false} />
          </span>
        ) : null}
      </p>
      {proposal.outcome !== "" && !auto && <p className="decided__detail">{proposal.outcome}</p>}
      {proposal.stale_reason !== "" && <p className="decided__detail">Stale: {proposal.stale_reason}</p>}
    </div>
  );
}

interface DecidedProposalsProps {
  // Null when the ticket filter leaves no status, so nothing can match.
  filter: ProposalTicketFilter | null;
}

export default function DecidedProposals({ filter }: DecidedProposalsProps) {
  const [state, setState] = useState<DecidedProposalState>("approved");
  const { status, error, proposals, hasMore, loadingMore, loadMore } = useDecidedProposals(filter, state);

  return (
    <div className="decided">
      <Dropdown<DecidedProposalState> label="State" value={state} options={STATE_OPTIONS} onChange={setState} />

      {status === "loading" && <p>Loading proposals…</p>}
      {error !== null && (
        <p role="alert" className="decided__error">
          {error}
        </p>
      )}
      {status === "loaded" && proposals.length === 0 && (
        <p className="decided__empty">
          {filter === null
            ? "Every status is hidden, so no tickets match."
            : `No ${STATE_LABEL.get(state)?.toLowerCase()} proposals.`}
        </p>
      )}
      {proposals.length > 0 && (
        <VirtualList
          fill
          items={proposals}
          getKey={(p) => p.id}
          renderRow={(p) => (
            <div className="decided__slot">
              <DecidedCard proposal={p} />
            </div>
          )}
          label="Decided proposals"
          estimateRowHeight={CARD_HEIGHT}
          measureRows
          hasMore={hasMore}
          loading={loadingMore}
          onLoadMore={loadMore}
          mode="button"
        />
      )}
    </div>
  );
}
