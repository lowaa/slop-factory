import type { TicketAttempt } from "@/api";

// Attempts that are running now: started, and neither closed nor done.
const LIVE_STATUSES: readonly string[] = [
  "in_progress",
  "discussion",
  "review",
];

// editWarning is what to say before the spec changes under attempts, or null
// when it doesn't: a lone attempt has nothing to differ from, so only two or
// more live attempts warn. It names each live attempt's starting revision so
// the person sees which ones will be working from a spec that no longer
// matches.
export function editWarning(
  attempts: readonly TicketAttempt[] | undefined,
): string | null {
  const live = (attempts ?? []).filter(
    (a) => a.started_revision != null && LIVE_STATUSES.includes(a.status),
  );
  if (live.length < 2) return null;
  const started = live.map((a, i) =>
    i === 0
      ? `Attempt ${a.number} started from revision ${a.started_revision}`
      : `attempt ${a.number} from revision ${a.started_revision}`,
  );
  return `${started.join(", ")}. Editing changes the spec under them.`;
}
