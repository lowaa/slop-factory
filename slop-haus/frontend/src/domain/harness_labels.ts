import type { HarnessSettingDefinition, HarnessSettings } from "@/api";
import { HARNESS_LABELS } from "./harnesses";

// summarise is one line per harness that has anything set: "Claude Code:
// Main model sonnet, Effort high".
export function summarise(definitions: HarnessSettingDefinition[], settings: HarnessSettings | undefined): string[] {
  const lines = new Map<string, string[]>();
  for (const d of definitions) {
    const value = settings?.[d.harness]?.[d.name];
    if (!value) continue;
    if (!lines.has(d.harness)) lines.set(d.harness, []);
    lines.get(d.harness)!.push(`${d.label} ${value}`);
  }
  return Array.from(lines, ([harness, parts]) => `${HARNESS_LABELS[harness] ?? harness}: ${parts.join(", ")}`);
}
