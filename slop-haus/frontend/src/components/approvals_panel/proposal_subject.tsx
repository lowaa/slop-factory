import type { Proposal } from "@/api";
import TicketLink from "@/shared/ticket_link/ticket_link";

interface ProposalSubjectProps {
  proposal: Proposal;
  // The class of the link, and of the id inside it; a ticketless proposal
  // shows the same text unlinked, under the same classes.
  className: string;
  idClassName: string;
}

export default function ProposalSubject({ proposal, className, idClassName }: ProposalSubjectProps) {
  if (proposal.ticket_id === null) {
    return (
      <span className={className}>
        <span className={idClassName}>Workspace</span>
      </span>
    );
  }
  return (
    <TicketLink ticketId={proposal.ticket_id} from="approvals" className={className}>
      <span className={idClassName}>#{proposal.ticket_id}</span> {proposal.ticket_title}
    </TicketLink>
  );
}
