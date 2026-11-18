import type { Status, Ticket } from "@/api";
import { statusLabel } from "@/domain/board_columns";

type Movable = Pick<Ticket, "status" | "allowed_moves" | "can_ready">;

// commonMoves is every status all of tickets may be moved to, from the
// server's allowed_moves rather than a copy of its rules, in board order.
// A status every one of them is already in is left out: moving there
// would change nothing.
export function commonMoves(tickets: readonly Movable[]): Status[] {
  if (tickets.length === 0) return [];
  return tickets[0].allowed_moves.filter(
    (to) => tickets.every((t) => t.allowed_moves.includes(to)) && !tickets.every((t) => t.status === to),
  );
}

// canReadyAll is whether Ready applies to every one of tickets: the bulk
// Ready is all or nothing, so one that can't be readied refuses the batch.
export function canReadyAll(tickets: readonly Movable[]): boolean {
  return tickets.length > 0 && tickets.every((t) => t.can_ready);
}

// noCommonMoveReason says why a selection has nothing to offer, naming the
// columns it spans -- the cue for which tickets to drop from it.
export function noCommonMoveReason(tickets: readonly Movable[]): string {
  const statuses = [...new Set(tickets.map((t) => t.status))].map(statusLabel);
  if (statuses.length < 2) return "There's no move these tickets can make.";
  return `No move fits them all: ${joinWords(statuses)} tickets allow different moves.`;
}

function joinWords(words: string[]): string {
  return words.length <= 1 ? words.join("") : `${words.slice(0, -1).join(", ")} and ${words[words.length - 1]}`;
}
