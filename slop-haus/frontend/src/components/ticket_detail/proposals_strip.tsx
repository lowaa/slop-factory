import { Link } from "@slop-factory/router";
import { pendingFor, useProposals } from "@/components/proposals";
import "./proposals_strip.scss";

// ProposalsStrip says a console has proposed something for this ticket, so a
// person reading the ticket knows there is a decision waiting. The action
// line is the server's own; deciding is done on the Approvals tab, where the
// agent's full text is shown, not here.
export default function ProposalsStrip({ ticketId }: { ticketId: number }) {
  const { proposals } = useProposals();
  const here = pendingFor(proposals, ticketId);
  if (here.length === 0) return null;
  return (
    <div className="proposals-strip" role="status">
      {here.map((p) => (
        <p key={p.id} className="proposals-strip__line">
          <strong>Proposed by {p.agent_name}:</strong> {p.action_line}{" "}
          <Link to="/approvals">Review on the Approvals tab</Link>
        </p>
      ))}
    </div>
  );
}
