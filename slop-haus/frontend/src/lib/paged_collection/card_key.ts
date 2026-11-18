import type { Ticket } from "@/api";

// The board's work columns list a ticket's attempts as cards of their own
// (see the attempt block on a Ticket), so a ticket number no longer names a
// card: two cards of one ticket share it. A card is (ticket, attempt); a
// ticket card, with no attempt block, is just the ticket.
export function cardKey(card: Ticket): string {
  return card.attempt ? `${card.id}/${card.attempt.number}` : String(card.id);
}

// What a ticket is drawn as, given the attempt cards the server sent for it
// (all of its cards in the work columns, or none): its cards if it has any,
// else the ticket card. A multi-attempt ticket's own status is one of the
// work statuses too, so the ticket card must not be drawn beside its
// attempts' cards.
export function cardsOf(ticket: Ticket, cards: readonly Ticket[]): Ticket[] {
  return cards.length > 0 ? [...cards] : [ticket];
}
