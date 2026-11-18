import { useEffect, useRef, useState, type RefObject } from "react";
import { apiClient, ApiError, type Ticket } from "@/api";

// Only the whole box: "#85 auth" and a bare "85" stay plain text searches.
const JUMP_PATTERN = /^#(\d+)$/;

export function parseTicketJump(text: string): number | null {
  const match = JUMP_PATTERN.exec(text.trim());
  if (!match) return null;
  const id = Number(match[1]);
  return Number.isSafeInteger(id) && id > 0 ? id : null;
}

export type TicketJumpResult = { kind: "found"; ticket: Ticket } | { kind: "missing"; id: number };

export interface TicketJumpState {
  // What to offer for the box as it is now: null while it is not a
  // ticket number, while the lookup is in flight, after it failed for a
  // reason other than there being no such ticket, or once dismissed.
  result: TicketJumpResult | null;
  // The offered ticket's link, which open() clicks.
  linkRef: RefObject<HTMLAnchorElement | null>;
  // open follows the offered ticket's link; false when none is offered.
  open: () => boolean;
  // dismiss hides the offer until the box changes; false when nothing was
  // showing.
  dismiss: () => boolean;
}

interface Lookup {
  id: number;
  result: TicketJumpResult | null;
}

// useTicketJump looks up the ticket the box names, straight from what is
// typed rather than after the board filter's debounce: a jump is to one
// ticket, so there is no list to spare re-fetching.
export function useTicketJump(text: string): TicketJumpState {
  const id = parseTicketJump(text);
  // Keyed by the number it was fetched for, so an answer for #8 landing
  // after the box moved on to #85 is never offered for it.
  const [lookup, setLookup] = useState<Lookup | null>(null);
  const [dismissed, setDismissed] = useState(false);
  const [seenText, setSeenText] = useState(text);
  if (text !== seenText) {
    setSeenText(text);
    setDismissed(false);
  }
  const linkRef = useRef<HTMLAnchorElement | null>(null);

  useEffect(() => {
    if (id === null) return;
    let current = true;
    apiClient.getTicket(id).then(
      (ticket) => {
        if (current) setLookup({ id, result: { kind: "found", ticket } });
      },
      (err: unknown) => {
        if (!current) return;
        // Only a 404 says there is no such ticket; anything else says
        // nothing about it, so nothing is offered.
        const missing = err instanceof ApiError && err.status === 404;
        setLookup({ id, result: missing ? { kind: "missing", id } : null });
      },
    );
    return () => {
      current = false;
    };
  }, [id]);

  const result = id !== null && !dismissed && lookup?.id === id ? lookup.result : null;

  return {
    result,
    linkRef,
    open: () => {
      if (result?.kind !== "found" || !linkRef.current) return false;
      linkRef.current.click();
      return true;
    },
    dismiss: () => {
      if (result === null) return false;
      setDismissed(true);
      return true;
    },
  };
}
