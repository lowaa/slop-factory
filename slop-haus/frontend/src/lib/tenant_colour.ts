import type { Tenant } from "@/api";

export type TenantColour = NonNullable<Tenant["colour"]>;

// Every key the server accepts, with its name for people. A Record, so a
// colour added to the API type can't be left out of the picker. The values
// themselves are the --tenant-<key>-bg/-fg tokens in index.scss.
export const TENANT_COLOUR_LABELS: Record<TenantColour, string> = {
  red: "Red",
  orange: "Orange",
  amber: "Amber",
  green: "Green",
  teal: "Teal",
  blue: "Blue",
  violet: "Violet",
  pink: "Pink",
};

export const TENANT_COLOURS = Object.keys(TENANT_COLOUR_LABELS) as TenantColour[];

// defaultTenantColour is the colour of a tenant whose owner chose none: a
// stable hash (FNV-1a) of its id into the palette, so every member sees the
// same one and it never changes under them.
export function defaultTenantColour(tenantId: string): TenantColour {
  let hash = 0x811c9dc5;
  for (let i = 0; i < tenantId.length; i++) {
    hash ^= tenantId.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return TENANT_COLOURS[hash % TENANT_COLOURS.length];
}

// tenantColour is the colour to draw: the owner's choice, else the default.
export function tenantColour(tenant: Pick<Tenant, "id" | "colour">): TenantColour {
  return tenant.colour ?? defaultTenantColour(tenant.id);
}

// tenantInitials is up to two letters, from the first two words of the name.
export function tenantInitials(name: string): string {
  const letters = name
    .split(/\s+/)
    .filter((word) => word !== "")
    .slice(0, 2)
    .map((word) => Array.from(word)[0].toUpperCase());
  return letters.join("") || "?";
}
