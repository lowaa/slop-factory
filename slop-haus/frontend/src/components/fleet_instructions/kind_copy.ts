import type { FleetInstructionsKind, Setting } from "@/api";

export interface KindCopy {
  tab: string;
  heading: string;
  hint: string;
  fieldLabel: string;
  // The sensitive word list may be saved empty, which is how an owner clears it.
  allowEmpty: boolean;
}

export const KIND_ORDER: readonly FleetInstructionsKind[] = ["fleet", "steward", "sensitive_words"];

export const KIND_COPY: Record<FleetInstructionsKind, KindCopy> = {
  fleet: {
    tab: "Fleet",
    heading: "Fleet instructions",
    hint: "Added to every agent’s briefing, after slop-haus’s own protocol, which takes precedence where they conflict. Agents pick up the default the next time their workspace is updated. A saved version never changes; saving adds the next one of its name, and does not change the default unless you say so.",
    fieldLabel: "Instructions",
    allowEmpty: false,
  },
  steward: {
    tab: "Steward",
    heading: "Steward instructions",
    hint: "These guide the steward’s judgment; they can’t loosen the server’s checks (protected paths, size limit, CI, the sensitive word list). A saved version never changes; saving adds the next one of its name, and does not change the default unless you say so.",
    fieldLabel: "Instructions",
    allowEmpty: false,
  },
  sensitive_words: {
    tab: "Sensitive words",
    heading: "Sensitive words",
    hint: "One word or phrase per line, matched case-insensitively and as whole words. A change that mentions one is never applied without a human. A saved version never changes; saving adds the next one of its name, and does not change the default unless you say so.",
    fieldLabel: "Words",
    allowEmpty: true,
  },
};

export const EMPTY_WORD_LIST_WARNING =
  "No sensitive words are pinned, yet the steward may decide judgment categories without a human. Pin a word list so sensitive changes still wait for you.";

// Judgment categories apply without a human from steward level 4 up. Reading
// the level here, rather than importing the tenant settings page's helpers,
// keeps this widget independent of that page; the settings API lists no
// categories, so a per-category override is not weighed. Advice only: an
// unreadable setting counts as no warning.
const JUDGMENT_FROM_LEVEL = 4;

// The deprecated steward_mode and steward_level are decided as the server does
// (steward/settings.go levelFrom): whichever was written last wins.
function stewardLevel(settings: Setting[]): number {
  const level = settings.find((s) => s.name === "steward_level");
  const mode = settings.find((s) => s.name === "steward_mode");
  const n = level?.int_val;
  const levelSet = n != null && n >= 0 && n <= 5 && !level?.is_default;
  const modeNewer = !!mode?.updated_at && (!level?.updated_at || mode.updated_at > level.updated_at);
  if (levelSet && !modeNewer) return n;
  if (mode?.string_val === "live") return 3;
  if (mode?.string_val === "shadow") return 1;
  return 0;
}

export function judgmentAboveHuman(settings: Setting[]): boolean {
  return stewardLevel(settings) >= JUDGMENT_FROM_LEVEL;
}

export function pinnedWordListEmpty(data: { default_id: string | null; versions: { id: string; body: string }[] }) {
  if (data.default_id === null) return true;
  const pinned = data.versions.find((v) => v.id === data.default_id);
  // A pinned version hidden because it is archived is still pinned.
  return pinned !== undefined && pinned.body.trim() === "";
}
