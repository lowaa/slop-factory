import { createContext, useContext } from "react";
import type { Proposal } from "@/api";

// The pending proposals, as ProposalsProvider keeps them: one list for the
// whole signed-in app, so the tab's badge, the Approvals tab and a ticket's
// strip show the same thing from one fetch and one stream.
export interface ProposalsValue {
  status: "loading" | "error" | "loaded";
  error: string | null;
  // Pending only, oldest first: the order a person would work through them.
  proposals: Proposal[];
  reload: () => Promise<void>;
}

export const ProposalsContext = createContext<ProposalsValue>({
  status: "loading",
  error: null,
  proposals: [],
  reload: async () => {},
});

export function useProposals(): ProposalsValue {
  return useContext(ProposalsContext);
}

// A proposal the ticket has moved on from: approving it will be refused, so
// the person is offered to dismiss it instead.
export function isStale(proposal: Proposal): boolean {
  return proposal.stale_reason !== "";
}

// subjectName is what a proposal is about, in words: a ticket by number, or
// the workspace for the proposals that have no ticket.
export function subjectName(p: Proposal): string {
  return p.ticket_id === null ? "the workspace" : `#${p.ticket_id}`;
}

export function pendingFor(proposals: Proposal[], ticketId: number): Proposal[] {
  return proposals.filter((p) => p.ticket_id === ticketId);
}

// The outcomes the server records on a proposal it applied without a click:
// at once under the owner's auto-approve setting (pendingaction.OutcomeAutoApproved)
// or by the steward (pendingaction.OutcomeStewardApproved). The state is
// "approved" either way, so this is the only way to tell them from a click.
const AUTO_OUTCOMES = new Set(["auto-approved", "steward-approved"]);

export function isAutoApproved(proposal: Pick<Proposal, "outcome">): boolean {
  return AUTO_OUTCOMES.has(proposal.outcome);
}
