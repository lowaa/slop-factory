import type { TicketAttempt } from "@/api";

// Finished is the server's: waiting on review, or over (done, or closed).
export function judgeable(attempt: TicketAttempt): boolean {
  return attempt.status === "review" || attempt.status === "done" || attempt.status === "closed";
}
