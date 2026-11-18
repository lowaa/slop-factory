import type { Setting } from "@/api";

export const STEWARD_LEVEL = "steward_level";
export const STEWARD_WARN_MIN_DECISIONS = "steward_warn_min_decisions";
export const STEWARD_WARN_MIN_AGREEMENT_PCT = "steward_warn_min_agreement_pct";
export const STEWARD_MODE = "steward_mode";
export const STEWARD_VETO_MINUTES = "steward_veto_minutes";
export const STEWARD_DAILY_CAP = "steward_daily_cap";
export const STEWARD_CATEGORY_TIERS = "steward_category_tiers";
export const STEWARD_REVIEW_MAX_LINES = "steward_review_max_lines";

export const STEWARD_SETTING_NAMES: ReadonlySet<string> = new Set([
  STEWARD_LEVEL,
  STEWARD_WARN_MIN_DECISIONS,
  STEWARD_WARN_MIN_AGREEMENT_PCT,
  STEWARD_MODE,
  STEWARD_VETO_MINUTES,
  STEWARD_DAILY_CAP,
  STEWARD_CATEGORY_TIERS,
  STEWARD_REVIEW_MAX_LINES,
]);

// Bounds the server enforces (steward/config.go).
export const VETO_MINUTES_BOUNDS = { min: 5, max: 1440, fallback: 60 };
export const DAILY_CAP_BOUNDS = { min: 0, max: 10_000, fallback: 50 };
export const REVIEW_MAX_LINES_BOUNDS = { min: 1, max: 100_000, fallback: 400 };

export const WARN_DECISIONS_BOUNDS = { min: 0, max: 10_000, fallback: 20 };
export const WARN_AGREEMENT_BOUNDS = { min: 0, max: 100, fallback: 95 };

export type Tier = "auto" | "veto" | "human";
export type CategoryClass = "mechanical" | "housekeeping" | "judgment";
export const TIERS: readonly Tier[] = ["auto", "veto", "human"];

export type StewardLevel = 0 | 1 | 2 | 3 | 4 | 5;

export interface LevelInfo {
  level: StewardLevel;
  name: string;
  description: string;
  example: string;
}

// Mirrors steward/level.go.
export const STEWARD_LEVELS: readonly LevelInfo[] = [
  {
    level: 0,
    name: "Off",
    description: "The steward does nothing.",
    example: "Every approval waits for you.",
  },
  {
    level: 1,
    name: "Shadow",
    description: "Assesses each proposal and records whether you agreed with it; acts on nothing.",
    example: "It notes it would have told an agent to merge; you decide, and the agreement record grows.",
  },
  {
    level: 2,
    name: "Assist",
    description: "Reads each proposal and pre-drafts the decision for you to approve with one click; never applies.",
    example: "A drafted answer to a discussion waits on your click.",
  },
  {
    level: 3,
    name: "Routine",
    description:
      "Mechanical categories apply at once, housekeeping after the veto window. Judgment categories stay yours, with a draft; opening a ticket is human-with-draft, since it is folded into marking a ticket ready.",
    example:
      "An agent asks whether to merge a green change and is told to carry on. A ticket it filed waits for you, with a draft.",
  },
  {
    level: 4,
    name: "Delegated review",
    description: "As level 3, plus judgment categories applied after the veto window unless you veto them.",
    example:
      "The steward approves a straightforward review after the veto window unless you veto it, e.g. a docs-only change with CI green and its slop-review pass clean.",
  },
  {
    level: 5,
    name: "Full delegation",
    description:
      "As level 3, plus judgment categories applied at once, inside the envelope; housekeeping stays at veto.",
    example: "A clean, in-scope review is approved and the change merges with no human look.",
  },
];

export const CONFIRM_FROM_LEVEL: StewardLevel = 4;

// Whichever of the level and the deprecated mode was written last decides, as
// the server does (steward/settings.go levelFrom): the mode can still be written
// elsewhere.
export function levelOf(level: Setting | undefined, mode: Setting | undefined): StewardLevel {
  const n = level?.int_val;
  const levelSet = n != null && n >= 0 && n <= 5 && !level?.is_default;
  const modeNewer = !!mode?.updated_at && (!level?.updated_at || mode.updated_at > level.updated_at);
  if (levelSet && !modeNewer) return n as StewardLevel;
  if (mode?.string_val === "live") return 3;
  if (mode?.string_val === "shadow") return 1;
  return 0;
}

export interface StewardCategory {
  id: string;
  label: string;
  neverAuto: boolean;
  class: CategoryClass;
}

// Mirrors steward/category.go's allowlist: the settings API lists no
// categories, and the server refuses an id it does not know, so a category
// missing here is merely not editable from the board.
export const STEWARD_CATEGORIES: readonly StewardCategory[] = [
  {
    id: "merge_continue",
    label: "Tell an agent to carry on and merge",
    neverAuto: false,
    class: "mechanical",
  },
  {
    id: "infra_wait",
    label: "Tell an agent to wait on a delayed outside system",
    neverAuto: false,
    class: "mechanical",
  },
  {
    id: "gate_confirmed",
    label: "Confirm a gate the agent asked about",
    neverAuto: false,
    class: "mechanical",
  },
  {
    id: "bookkeeping",
    label: "Blockers and child tickets from an approved plan",
    neverAuto: false,
    class: "mechanical",
  },
  {
    id: "narrow_scope",
    label: "Narrow a ticket's paths",
    neverAuto: false,
    class: "housekeeping",
  },
  {
    id: "priority",
    label: "Change a ticket's priority",
    neverAuto: false,
    class: "housekeeping",
  },
  {
    id: "ticket_ready",
    label: "Open a preparing ticket",
    neverAuto: false,
    class: "judgment",
  },
  {
    id: "discussion_pick",
    label: "Accept the option a worker recommends",
    neverAuto: false,
    class: "judgment",
  },
];

export type TierOverrides = Record<string, Tier>;

// A stored value the board cannot read is shown as no overrides rather than
// failing the section; the server validated what it stored.
export function parseTierOverrides(raw: string | null): TierOverrides {
  try {
    const parsed: unknown = JSON.parse(raw || "{}");
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return {};
    return Object.fromEntries(Object.entries(parsed).filter((e): e is [string, Tier] => TIERS.includes(e[1] as Tier)));
  } catch {
    return {};
  }
}

// Only the differences from the level's default are stored, so a default that
// changes later is followed.
export function withTier(
  overrides: TierOverrides,
  category: StewardCategory,
  tier: Tier,
  levelDefault: Tier,
): TierOverrides {
  const next = { ...overrides };
  if (tier === levelDefault) delete next[category.id];
  else next[category.id] = tier;
  return next;
}

export function settingNamed(settings: Setting[], name: string): Setting | undefined {
  return settings.find((s) => s.name === name);
}

export interface CategoryRecord {
  assessed: number;
  // Percent, null before the owner has decided anything.
  agreementPct: number | null;
}

export interface DigestTotals {
  assessed: number;
  needs_human: number;
  unclassified: number;
  awaiting: number;
  agreed: number;
  disagreed: number;
}

// Says what became of the window's assessments, so an empty agreement record
// explains itself: only verdicts that claim something, which the owner then
// decides, can ever count towards it.
export function agreementSummary(t: DigestTotals): string {
  if (t.assessed === 0) return "Nothing assessed in the last 7 days.";
  const parts: string[] = [];
  if (t.needs_human > 0) {
    parts.push(`${t.needs_human} yours by design${t.unclassified > 0 ? ` (${t.unclassified} unclassified)` : ""}`);
  }
  if (t.awaiting > 0) parts.push(`${t.awaiting} awaiting your decision`);
  if (t.agreed + t.disagreed > 0) parts.push(`${t.agreed} agreed, ${t.disagreed} disagreed`);
  return `${t.assessed} assessed in the last 7 days${parts.length > 0 ? ` — ${parts.join(", ")}` : ""}`;
}

export interface WarningInput {
  level: StewardLevel;
  overrides: TierOverrides;
  categories: readonly StewardCategory[];
  record: (categoryId: string) => CategoryRecord | undefined;
  minDecisions: number;
  minAgreementPct: number;
  // Null when the manifest could not be read: the warning is then left out.
  protectedPathRules: number | null;
  // Whether the pinned sensitive word list has no words; null when it could not
  // be read: the warning is then left out.
  sensitiveWordsEmpty: boolean | null;
}

export const ENVELOPE_WARNING = "Changes inside the envelope will be approved and merged with no human look.";

// Mirrors steward/level.go Applies(): below Routine no tier but human applies.
export const TIERS_APPLY_FROM_LEVEL: StewardLevel = 3;

export function tiersApply(level: StewardLevel): boolean {
  return level >= TIERS_APPLY_FROM_LEVEL;
}

// Mirrors steward.EffectiveTier: below Routine nothing applies.
export function tierAtLevel(c: StewardCategory, level: StewardLevel, overrides: TierOverrides): Tier {
  if (c.neverAuto || !tiersApply(level)) return "human";
  const o = overrides[c.id];
  if (o) return o;
  if (c.class === "mechanical") return "auto";
  if (c.class === "housekeeping") return "veto";
  return level === 5 ? "auto" : level === 4 ? "veto" : "human";
}

// Advice only: nothing here blocks a save.
export function stewardWarnings(w: WarningInput): string[] {
  const out: string[] = [];
  const tier = (c: StewardCategory) => tierAtLevel(c, w.level, w.overrides);
  const raised = w.categories.filter((c) => tier(c) !== "human");
  const judgmentRaised = raised.filter((c) => c.class === "judgment");
  if (w.level === 0) out.push("The steward is off: nothing is assessed.");
  for (const c of raised) {
    const r = w.record(c.id);
    if (!r || r.assessed === 0) out.push(`${c.label}: shadow has not run for this category yet.`);
    else if (r.assessed < w.minDecisions || (r.agreementPct !== null && r.agreementPct < w.minAgreementPct))
      out.push(
        `${c.label}: ${r.assessed} decisions so far (suggested at least ${w.minDecisions}), agreement ${r.agreementPct === null ? "unknown" : `${r.agreementPct}%`} (suggested at least ${w.minAgreementPct}%).`,
      );
  }
  if (w.protectedPathRules === 0 && judgmentRaised.length > 0)
    out.push("No protected paths are set, so no path stops a judgment decision. Set them in the Workspace manifest.");
  if (w.sensitiveWordsEmpty && judgmentRaised.length > 0)
    out.push("The sensitive word list is empty or none is pinned, so no topic is held back from a judgment decision.");
  if (w.level === 5 || judgmentRaised.some((c) => tier(c) === "auto")) out.push(ENVELOPE_WARNING);
  return out;
}

// The stored overrides that raise a category above human, in category order,
// whether or not the current level lets them apply.
export function raisedOverrides(
  overrides: TierOverrides,
  categories: readonly StewardCategory[] = STEWARD_CATEGORIES,
): { category: StewardCategory; tier: Tier }[] {
  return categories.flatMap((category) => {
    const tier = overrides[category.id];
    return tier && tier !== "human" && !category.neverAuto ? [{ category, tier }] : [];
  });
}

// What saving needs the owner to confirm, or null when it needs nothing.
export function levelChangeConfirmation(
  from: StewardLevel,
  to: StewardLevel,
  overrides: TierOverrides = {},
): string | null {
  const parts: string[] = [];
  if (to >= CONFIRM_FROM_LEVEL && to !== from) {
    const info = STEWARD_LEVELS[to];
    parts.push(`Level ${from} -> ${to} (${info.name}). From now: ${info.description}${to === 5 ? ` ${ENVELOPE_WARNING}` : ""}`);
  }
  // Stored while the level ignored them, they take effect on this change.
  const waking = tiersApply(from) || !tiersApply(to) ? [] : raisedOverrides(overrides);
  if (waking.length > 0)
    parts.push(
      `Stored tier overrides will take effect: ${waking.map((o) => `${o.category.label} (${o.tier})`).join(", ")}.`,
    );
  return parts.length > 0 ? parts.join(" ") : null;
}

export function tierChangeConfirmation(c: StewardCategory, tier: Tier): string | null {
  if (c.class !== "judgment" || tier !== "auto") return null;
  return `${c.label} will apply at once. ${ENVELOPE_WARNING}`;
}
