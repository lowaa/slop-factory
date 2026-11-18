import type { VariantVersion } from "@/api";

export interface VariantDraft {
  name: string;
  harness: VariantVersion["harness"];
  settings: Record<string, string>;
  // null is the explicit "none"; the tenant's default is not an option.
  fleetVersionId: string | null;
}

export const EMPTY_DRAFT: VariantDraft = { name: "", harness: "claude", settings: {}, fleetVersionId: null };
