import type { Ticket } from "@/api";
import { SharedOverlapTag } from "@/components/shared_overlap_tag";
import "./scope_overlaps.scss";

interface ScopeOverlapsProps {
  ticket: Ticket;
}

// Shown only while a human can still do something about it: a ticket in
// preparing or open has not yet been held. The server sends nothing for any
// other status, and a conflict is sometimes real, so this informs rather
// than blocks.
export default function ScopeOverlaps({ ticket }: ScopeOverlapsProps) {
  const overlaps = ticket.scope_overlaps ?? [];
  if (overlaps.length === 0) return null;
  return (
    <section className="ticket-detail__scope-overlaps" aria-label="Scope overlaps">
      <h2>Scope overlaps</h2>
      <p className="ticket-detail__hint">
        These live tickets cover paths this ticket also covers, so they can't be claimed in parallel, unless an
        overlap is marked shared. Narrower paths (leaf directories or files) avoid that; a real conflict is fine.
      </p>
      <ul className="ticket-detail__depends">
        {overlaps.map((o) => (
          <li key={o.ticket_id}>
            <span>
              #{o.ticket_id} {o.title} ({o.status}): {o.path} overlaps {o.other}
              <SharedOverlapTag resolve={o.resolve} />
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
