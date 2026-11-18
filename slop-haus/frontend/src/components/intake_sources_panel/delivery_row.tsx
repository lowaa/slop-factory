import type { IntakeDelivery } from "@/api";
import Timestamp from "@/shared/timestamp/timestamp";
import TicketLink from "@/shared/ticket_link/ticket_link";
import "./delivery_row.scss";

interface DeliveryRowProps {
  delivery: IntakeDelivery;
  sourceName: string;
}

// key and detail are whatever the sender put there, so they go in as plain
// text children -- never markup, markdown or a link.
export default function DeliveryRow({ delivery, sourceName }: DeliveryRowProps) {
  return (
    <li className="delivery-row">
      <Timestamp at={delivery.received_at} />
      <span className="delivery-row__meta">{sourceName}</span>
      <span className="delivery-row__event">{delivery.event}</span>
      <span className="delivery-row__key">{delivery.key}</span>
      <span
        className={`delivery-row__outcome delivery-row__outcome--${delivery.outcome}`}
      >
        {delivery.outcome}
      </span>
      {delivery.downgraded && (
        <span className="delivery-row__outcome delivery-row__outcome--downgraded">
          downgraded to preparing
        </span>
      )}
      {delivery.ticket_id !== null && (
        <TicketLink ticketId={delivery.ticket_id} from="intake">
          #{delivery.ticket_id}
        </TicketLink>
      )}
      <span className="delivery-row__detail">{delivery.detail}</span>
    </li>
  );
}
