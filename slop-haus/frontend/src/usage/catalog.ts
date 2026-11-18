// The one list of what the board's browser may report as usage. An event is a
// name with the attributes it may carry, and an attribute is an enum, a flag
// or a rounded number: never free text, a name, an id or anything someone
// typed, so nothing here can say who did something or what their data was.
// The server only counts these (slophaus_ui_events_total), and holds no user,
// tenant or session id with a count.
//
// Two things are derived from this file and nothing else lists them:
//  - the types of useUsage().event(), so an unknown event or attribute does
//    not compile;
//  - server/internal/usagemetrics/ui_allowlist.json, which the server
//    enforces on what it counts, and which a test checks against this file.

// What each screen is called, by the route it is (screens.ts maps a route to
// one). Never a URL or an id.
export const SCREENS = [
  "board",
  "remote_control",
  "blocked",
  "approvals",
  "steward",
  "unreviewed",
  "reports",
  "compare",
  "workspace",
  "github_app_setup",
  "workers",
  "add_machine",
  "members",
  "tenant_settings",
  "git",
  "intake",
  "eval_sets",
  "ticket",
  "ticket_attempt",
  "account",
  "new_tenant",
  "help",
  "help_page",
  "legal",
] as const;
export type Screen = (typeof SCREENS)[number];

// Counts are bucketed: an exact number of someone's tickets is more than
// "how much is bulk action used" needs.
export const COUNT_BUCKETS = ["0", "1", "2-5", "6-20", "21+"] as const;
export type CountBucket = (typeof COUNT_BUCKETS)[number];

export function countBucket(count: number): CountBucket {
  if (count <= 0) return "0";
  if (count === 1) return "1";
  if (count <= 5) return "2-5";
  if (count <= 20) return "6-20";
  return "21+";
}

// Visible time is rounded to this and capped, so an idle tab left open
// overnight is one outlier value rather than a skew, and the exact length of
// a visit is not recorded.
export const VISIBLE_MS_STEP = 5_000;
export const VISIBLE_MS_MAX = 30 * 60 * 1000;

export function roundVisibleMs(ms: number): number {
  const rounded = Math.round(ms / VISIBLE_MS_STEP) * VISIBLE_MS_STEP;
  return Math.min(Math.max(rounded, 0), VISIBLE_MS_MAX);
}

export interface EnumSpec {
  readonly type: "enum";
  readonly values: readonly string[];
  readonly optional?: boolean;
}
export interface BoolSpec {
  readonly type: "bool";
  readonly optional?: boolean;
}
export interface IntSpec {
  readonly type: "int";
  // Values are multiples of step, from 0 to max.
  readonly max: number;
  readonly step: number;
  readonly optional?: boolean;
}
export type AttrSpec = EnumSpec | BoolSpec | IntSpec;

const oneOf = <const V extends readonly string[]>(values: V) => ({ type: "enum", values }) as const;
const count = oneOf(COUNT_BUCKETS);
const screen = oneOf(SCREENS);
const bulkOutcome = oneOf(["done", "refused", "error"]);

// Attribute names are shared across events where they mean the same thing,
// because each becomes a label of the one counter: keep the set small.
export const CATALOG = {
  // A screen was shown: on a route change, from the route's template.
  screen_view: { screen },
  // Visible time on a screen, once when it is left (and when the page is
  // hidden): see screen_time.ts. Feeds a histogram, not the event counter.
  screen_time: { screen, visible_ms: { type: "int", max: VISIBLE_MS_MAX, step: VISIBLE_MS_STEP } },
  // The "Answer with steward" widget on a ticket's discussion.
  steward_answer: { screen, action: oneOf(["open", "assess", "approve", "edit", "reject", "veto"]) },
  proposal_decision: { screen, action: oneOf(["approve", "reject", "apply_steward_draft"]) },
  remote_control: { screen, action: oneOf(["start_session", "send_message"]) },
  // Never the text typed or the value chosen. has_text is for the search,
  // where "was anything typed" is the whole signal.
  filter_used: {
    screen,
    filter: oneOf(["ticket_text", "ticket_field", "ticket_clear", "reports_period", "reports_priority", "reports_parked"]),
    has_text: { type: "bool", optional: true },
  },
  bulk_action: { screen, action: oneOf(["move", "mark_ready", "priority"]), outcome: bulkOutcome, count_bucket: count },
  new_ticket: { screen, action: oneOf(["open_form", "create_open", "create_preparing"]) },
  lock_action: { screen, action: oneOf(["force_unlock", "drop_waiter"]), outcome: oneOf(["done", "error"]) },
} as const satisfies Record<string, Record<string, AttrSpec>>;

export type Catalog = typeof CATALOG;
export type UsageEventName = keyof Catalog;

type ValueOf<S extends AttrSpec> = S extends { type: "enum"; values: infer V extends readonly string[] }
  ? V[number]
  : S extends { type: "bool" }
    ? boolean
    : S extends { type: "int" }
      ? number
      : never;

type OptionalKeys<A> = { [K in keyof A]: A[K] extends { optional: true } ? K : never }[keyof A];
type RequiredKeys<A> = Exclude<keyof A, OptionalKeys<A>>;

// The attributes an event takes: every one of its catalog entry, and no
// others.
export type EventAttrs<E extends UsageEventName> = {
  [K in RequiredKeys<Catalog[E]>]: ValueOf<Catalog[E][K] & AttrSpec>;
} & {
  [K in OptionalKeys<Catalog[E]>]?: ValueOf<Catalog[E][K] & AttrSpec>;
};
