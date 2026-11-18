// The harnesses slop-haus knows how to run, and the only place that list
// lives. The ids are the harness names the launchers and the harness config use.
//
// A page that explains something per harness types its text as
// HarnessContent<...>, so a harness added here without text for that page
// fails to compile there. Adding a harness is therefore one entry here and
// one block of text per page.
import type { components } from "@/api_generated/schema";

// The ids are the API's Harness enum, which derives from contracts/harnesses.json,
// so a harness added there fails to compile here until it has a label and binary.
type ApiHarness = components["schemas"]["Harness"];

interface HarnessFacts {
  label: string;
  // The executable that has to be on PATH.
  binary: string;
}

// Listed in display order: the first is the default.
const FACTS: Record<ApiHarness, HarnessFacts> = {
  claude: { label: "Claude Code", binary: "claude" },
  pi: { label: "pi", binary: "pi" },
  codex: { label: "Codex", binary: "codex" },
};

export const HARNESSES = (Object.entries(FACTS) as [ApiHarness, HarnessFacts][]).map(([id, facts]) => ({ id, ...facts }));

export type Harness = (typeof HARNESSES)[number];
export type HarnessId = ApiHarness;

export type HarnessContent<T> = Record<HarnessId, T>;

// The first one listed is the one a page opens on.
export const DEFAULT_HARNESS: HarnessId = HARNESSES[0].id;

export const HARNESS_LABELS: Record<string, string> = Object.fromEntries(HARNESSES.map((h) => [h.id, h.label]));

export function harnessById(id: HarnessId): Harness {
  return HARNESSES.find((h) => h.id === id) ?? HARNESSES[0];
}
