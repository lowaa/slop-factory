import type { ReactNode } from "react";
import type { AgentContext, Comment, Ticket, TicketAttempt } from "@/api";

export function attemptsOf(ticket: Ticket): TicketAttempt[] {
  return ticket.attempts ?? [];
}

// A ticket with one attempt is shown as it always was: no tabs, one
// conversation. Only a second attempt splits the page.
export function hasSeveralAttempts(ticket: Ticket): boolean {
  return attemptsOf(ticket).length > 1;
}

// The attempt a page is of, when it is of one: the address can name one a
// ticket with a single attempt (or none) does not split into tabs.
export function selectedAttempt(ticket: Ticket, number: number | undefined): TicketAttempt | undefined {
  if (number === undefined || !hasSeveralAttempts(ticket)) return undefined;
  return attemptsOf(ticket).find((a) => a.number === number);
}

// The ticket's own conversation is what was said to no attempt: the spec
// discussion before work started, and anything said to every live attempt.
// An attempt's is what was said to or by it alone.
export function commentsOfAttempt(comments: Comment[], attempt: number | null): Comment[] {
  return comments.filter((c) => c.attempt_number === attempt);
}

export function contextsOfAttempt(contexts: AgentContext[], attempt: number): AgentContext[] {
  return contexts.filter((c) => c.attempt_number === attempt);
}

// A finished attempt nobody has promoted waits for a choice between it and
// its siblings (the board card says the same).
export function awaitingChoice(attempt: TicketAttempt): boolean {
  return attempt.status === "review" && !attempt.promoted_at;
}

export interface AttemptLinkProps {
  className?: string;
  "aria-current"?: "page";
  children: ReactNode;
}

// How the page links to an attempt's tab (null: the ticket's own view). The
// caller's to make, like the Back link: it needs the router, which this only
// shows a ticket.
export type AttemptLink = (target: number | null, props: AttemptLinkProps) => ReactNode;

// What the title chip says of an attempt: its number, then what sets it
// apart from the ordinary one.
export function attemptChipText(attempt: Pick<TicketAttempt, "number" | "shadow" | "promoted">): string {
  const parts = [`attempt ${attempt.number}`];
  if (attempt.shadow) parts.push("shadow");
  if (attempt.promoted) parts.push("promoted");
  return parts.join(" · ");
}

// An attempt a person may close: not the promoted one (the ticket's own
// result), and not one already finished or closed.
export function closable(attempt: TicketAttempt): boolean {
  return !attempt.promoted && ["queued", "in_progress", "discussion", "review"].includes(attempt.status);
}

export function isClosed(attempt: Pick<TicketAttempt, "status">): boolean {
  return attempt.status === "closed";
}

// An attempt a person may promote: live or awaiting the choice, and not the
// promoted one already. The ticket's own state is the caller's to check.
export function promotable(attempt: TicketAttempt): boolean {
  return !attempt.promoted && ["queued", "in_progress", "discussion", "review"].includes(attempt.status);
}

// The attempt the ticket was done through, for an unpromoted attempt left in
// review after it: the owner may still read its work, but nothing awaits their
// choice any more, and only closing it clears it.
export function notChosenBy(ticket: Ticket, attempt: TicketAttempt): TicketAttempt | undefined {
  if (ticket.status !== "done" || attempt.promoted || attempt.status !== "review") return undefined;
  return attemptsOf(ticket).find((a) => a.promoted && a.number !== attempt.number);
}
