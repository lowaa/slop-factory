import type { KeyboardEvent, ReactNode } from "react";
import type { Ticket } from "@/api";
import { describeRequiredRoles, NO_ELIGIBLE_AGENT_WARNING } from "@/lib/roles";
import { liveRecheck } from "@/lib/cooldown";
import { describeMergeLock, LandingBadge, useLandingEntry, useMergeLockState } from "@/landing_state";
import PrLink from "@/shared/pr_link/pr_link";
import TicketLink from "@/shared/ticket_link/ticket_link";
import AttemptSection from "./attempt_section";
import RecheckAt from "./recheck_at";
import "./ticket_card.scss";

interface TicketCardProps {
  ticket: Ticket;
  // Set in the board's select mode: the card toggles the ticket in and out
  // of the selection instead of opening it.
  selection?: {
    selected: boolean;
    onToggle: () => void;
  };
}

// Out of select mode, a link to the ticket, not a <div onClick>: keyboard-navigable, announced
// as something to follow, and openable in a new tab.
//
// An attempt card (a ticket with several attempts, in a work column) is this
// same card with an attempt section: the card of one attempt, not of the
// ticket. It opens that attempt's tab.
//
// The PR link sits beside the ticket link rather than in it: links cannot
// nest, and browsers disagree on which of the two a click would follow.
export default function TicketCard({ ticket, selection }: TicketCardProps) {
  const content = <TicketCardContent ticket={ticket} />;
  return (
    <div className={`ticket-card${selection?.selected ? " ticket-card--selected" : ""}`}>
      {selection ? (
        <SelectToggle selected={selection.selected} onToggle={selection.onToggle}>
          {content}
        </SelectToggle>
      ) : (
        <TicketLink ticketId={ticket.id} from="board" attempt={ticket.attempt?.number} className="ticket-card__select">
          {content}
        </TicketLink>
      )}
      <PrLink url={ticket.pr_url} className="ticket-card__pr" ariaLabel={`PR for #${ticket.id}${ticket.attempt ? ` attempt ${ticket.attempt.number}` : ""}`}>
        PR
      </PrLink>
    </div>
  );
}

// A role="button" rather than a <button>: the card's content is block
// markup (a list of paths among it), which a <button> may not hold. So the
// keys a button answers to are wired up by hand.
function SelectToggle({
  selected,
  onToggle,
  children,
}: {
  selected: boolean;
  onToggle: () => void;
  children: ReactNode;
}) {
  function handleKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onToggle();
    }
  }
  return (
    <div
      role="button"
      tabIndex={0}
      aria-pressed={selected}
      className="ticket-card__select"
      onClick={onToggle}
      onKeyDown={handleKeyDown}
    >
      {children}
    </div>
  );
}

function TicketCardContent({ ticket }: { ticket: Ticket }) {
  const recheck = liveRecheck(ticket);
  const landing = useLandingEntry(ticket.id, ticket.attempt?.number);
  // Only a ticket in progress is worked by a worker, who is the one that takes the lock.
  const mergeLock = useMergeLockState(ticket.id);
  const lockLabel = ticket.status === "in_progress" && mergeLock ? describeMergeLock(mergeLock) : null;
  return (
    <>
      <div className="ticket-card__header">
        <span className="ticket-card__id">
          #{ticket.id}
          {ticket.attempt && ` · ${ticket.attempt.number}`}
        </span>
        <span className={`ticket-card__priority ticket-card__priority--${ticket.priority}`}>{ticket.priority}</span>
      </div>
      {/* An external ticket's own title is the board's ("[source] external report"); what the sender called it is text from outside, for a person to read here, with the badge saying so. */}
      <p className="ticket-card__title" title={ticket.external ? ticket.title : undefined}>
        {ticket.external?.title || ticket.title}
      </p>
      {ticket.attempt && <AttemptSection attempt={ticket.attempt} />}
      {(recheck ||
        landing ||
        lockLabel ||
        ticket.external ||
        ticket.needs_discussion ||
        ticket.requires_approval ||
        ticket.status === "discussion" ||
        ticket.status === "review" ||
        ticket.status === "preparing" ||
        ticket.status === "parked" ||
        ticket.required_roles.length > 0 ||
        ticket.no_eligible_agent) && (
        <div className="ticket-card__badges">
          <LandingBadge ticketId={ticket.id} attemptNumber={ticket.attempt?.number} className="ticket-card__badge" />
          {lockLabel && <span className="ticket-card__badge ticket-card__badge--merge-lock">{lockLabel}</span>}
          {recheck && (
            <span className="ticket-card__badge ticket-card__badge--recheck">
              <RecheckAt at={recheck} />
            </span>
          )}
          {ticket.external && <span className="ticket-card__badge ticket-card__badge--external">external</span>}
          {ticket.needs_discussion && (
            <span className="ticket-card__badge ticket-card__badge--discussion">needs discussion</span>
          )}
          {ticket.requires_approval && (
            <span className="ticket-card__badge ticket-card__badge--requires-approval">requires approval</span>
          )}
          {ticket.status === "discussion" && (
            <span className="ticket-card__badge ticket-card__badge--waiting">waiting on you</span>
          )}
          {ticket.status === "review" && (
            <span className="ticket-card__badge ticket-card__badge--waiting">waiting on your review</span>
          )}
          {ticket.status === "preparing" && (
            <span className="ticket-card__badge ticket-card__badge--preparing">not in queue</span>
          )}
          {ticket.status === "parked" && (
            <span className="ticket-card__badge ticket-card__badge--parked">parked -- not planned</span>
          )}
          {ticket.required_roles.length > 0 && (
            <span className="ticket-card__badge ticket-card__badge--role">
              {describeRequiredRoles(ticket.required_roles, ticket.role_match)}
            </span>
          )}
          {ticket.no_eligible_agent && (
            <span className="ticket-card__badge ticket-card__badge--no-eligible-agent" title={NO_ELIGIBLE_AGENT_WARNING}>
              no eligible agent
            </span>
          )}
        </div>
      )}
      {ticket.paths.length > 0 && (
        <ul className="ticket-card__paths">
          {ticket.paths.map((p) => (
            <li key={p.path}>{p.path}</li>
          ))}
        </ul>
      )}
    </>
  );
}
