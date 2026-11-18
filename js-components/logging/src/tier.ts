// The values are the ids of the consent purposes that gate them (see
// go-privacy/README.md), so the server-side proxy can enforce the same
// rule from the record alone.
export type Tier = "operational" | "analytics" | "interaction_recording";

export const TIER_ATTRIBUTE = "privacy.tier";
export const SESSION_ID_ATTRIBUTE = "session.id";
export const EVENT_NAME_ATTRIBUTE = "event.name";

// Set by the logger itself. A caller's attrs can never supply them, or a
// record could label itself operational to bypass the consent check.
export const RESERVED_ATTRIBUTES: ReadonlySet<string> = new Set([
  TIER_ATTRIBUTE,
  SESSION_ID_ATTRIBUTE,
  EVENT_NAME_ATTRIBUTE,
]);
