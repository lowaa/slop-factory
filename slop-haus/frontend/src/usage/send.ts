import { sessionWriteHeaders } from "@/api";
import type { UsageEvent, UsageSender } from "./usage";

export const USAGE_PATH = "/api/usage/events";

// keepalive lets the request outlive a page that is closing, which is when
// most batches are sent. The session's CSRF header is what the server's
// signed-in writes need; an answer (a 401 after sign-out, a 429) is ignored.
export const sendUsage: UsageSender = (events: UsageEvent[]) => {
  fetch(USAGE_PATH, {
    method: "POST",
    keepalive: true,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...sessionWriteHeaders() },
    body: JSON.stringify({ events }),
  }).catch(() => {});
};
