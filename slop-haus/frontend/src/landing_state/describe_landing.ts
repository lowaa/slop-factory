import type { LandingEntry } from "@/api";
import { formatDuration } from "@/lib/format_duration";

export interface LandingLabel {
  text: string;
  tone: "landing" | "back";
  // How long the landing has been in its step (or queued), "" once handed back.
  since: string;
  // The landing has been on a step past what the runner allows it, so it is
  // stuck rather than slow.
  stalled: boolean;
  // Longer words for the badge's tooltip.
  title: string;
}

// How long the runner gives a merge of main or the merge itself (the server's
// otherStepBound). Waiting for CI is bounded by the repo's own checks timeout,
// which only the server knows, so a CI wait is never called stalled here.
const OTHER_STEP_BOUND_SECONDS = 15 * 60;

const STEP_TEXT: Record<LandingEntry["step"], string> = {
  "": "queued",
  merging_main: "merging main",
  waiting_checks: "waiting for CI",
  merging: "merging",
};

export function describeLanding(entry: LandingEntry): LandingLabel {
  // A server from before the field existed (mid-rollout) leaves it out.
  const sinceUpdate = entry.since_update_seconds ?? 0;
  if (entry.state === "back_with_agent") {
    return {
      text: "back with agent",
      tone: "back",
      since: "",
      stalled: false,
      title: entry.back_reason
        ? `The server stopped landing this change and handed it back: ${entry.back_reason}`
        : "The server stopped landing this change and handed it back to its agent.",
    };
  }
  const step = STEP_TEXT[entry.step];
  // Position 1 is the change next to land; a begun landing shows its step instead.
  const base = entry.step === "" && entry.position > 0 ? `landing · #${entry.position} in queue` : `landing · ${step}`;
  const batch = entry.batch_id === null ? "" : ` · batch ${entry.batch_id} #${entry.batch_position}`;
  const title = "The server is landing this change: it merges main, waits for CI, then merges.";
  return {
    text: base + batch,
    tone: "landing",
    since: formatDuration(sinceUpdate),
    stalled: (entry.step === "merging_main" || entry.step === "merging") && sinceUpdate > OTHER_STEP_BOUND_SECONDS,
    title: batch ? `${title} It is tested in one CI run with the other changes of batch ${entry.batch_id}, and merged ${ordinal(entry.batch_position)} of them.` : title,
  };
}

export function ordinal(n: number): string {
  const rest = n % 100;
  if (rest >= 11 && rest <= 13) return `${n}th`;
  return `${n}${{ 1: "st", 2: "nd", 3: "rd" }[n % 10] ?? "th"}`;
}
