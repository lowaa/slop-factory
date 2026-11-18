import type { FleetInstructionsVersion, HarnessSettingDefinition, VariantVersion } from "@/api";
import { summarise } from "@/domain/harness_labels";

// settingsLine is a version's settings in one line ("Main model sonnet,
// Effort high"), or "no settings" when it sets none.
export function settingsLine(definitions: HarnessSettingDefinition[], v: VariantVersion): string {
  const [line] = summarise(definitions, { [v.harness]: v.settings });
  const text = line?.slice(line.indexOf(": ") + 2);
  return text || "no settings";
}

// pinnedLabel names the fleet instructions a version carries. "none" is an
// explicit choice, never the tenant's default.
export function pinnedLabel(fleetVersions: FleetInstructionsVersion[], id: string | null): string {
  if (id === null) return "none";
  const found = fleetVersions.find((f) => f.id === id);
  return found ? `${found.name} v${found.version}` : "an unknown version";
}
