import type { Ticket } from "@/api";
import type { LiveChanges, SubscribeChanges } from "@/lib/paged_collection/paged_collection";

// Hands the tickets the board stream names to every column's collection,
// each of which decides for itself whether the ticket belongs to it. One
// feed per board, so a column's collection subscribes to it like to any
// other source of live changes.
export class BoardTicketFeed {
  private readonly subscribers = new Set<LiveChanges>();

  subscribe: SubscribeChanges = (changes) => {
    this.subscribers.add(changes);
    return () => {
      this.subscribers.delete(changes);
    };
  };

  // cards are the attempt cards of those of the tickets that have several
  // attempts; each ticket is handed its own, so every column redraws all of
  // that ticket's cards.
  emit(tickets: readonly Ticket[], cards: readonly Ticket[] = []): void {
    for (const subscriber of [...this.subscribers]) {
      for (const ticket of tickets) {
        subscriber.onTicket(
          ticket,
          cards.filter((card) => card.id === ticket.id),
        );
      }
    }
  }
}
