import type { Ticket } from "@/api";

// The instant a blocked ticket is next re-checked by the queue, while that is
// still ahead. blocked_until stays on the ticket after it passes (until the
// next status write), and a ticket held only by its dependencies has none, so
// both read as no cooldown.
export function liveRecheck(ticket: Pick<Ticket, "status" | "blocked_until">, now: number = Date.now()): Date | null {
  if (ticket.status !== "blocked" || !ticket.blocked_until) return null;
  const at = new Date(ticket.blocked_until);
  return at.getTime() > now ? at : null;
}
