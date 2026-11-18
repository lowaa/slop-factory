import TicketLink from "@/shared/ticket_link/ticket_link";
import { statusLabel } from "@/domain/board_columns";
import type { TicketJumpState } from "./use_ticket_jump";
import "./ticket_jump.scss";

// TicketJump offers the ticket the search box names by number. The live
// region is always there, so a screen reader hears the offer arrive.
export default function TicketJump({ jump }: { jump: TicketJumpState }) {
  const { result } = jump;
  return (
    <div className="ticket-jump" aria-live="polite">
      {result?.kind === "found" && (
        <TicketLink ticketId={result.ticket.id} from="board" className="ticket-jump__link" ref={jump.linkRef}>
          <span className="ticket-jump__id">#{result.ticket.id}</span>
          <span className="ticket-jump__separator" aria-hidden="true">
            ·
          </span>
          <span className="ticket-jump__title">{result.ticket.title}</span>
          <span className="ticket-jump__status">{statusLabel(result.ticket.status)}</span>
        </TicketLink>
      )}
      {result?.kind === "missing" && <p className="ticket-jump__missing">No ticket #{result.id}</p>}
    </div>
  );
}
