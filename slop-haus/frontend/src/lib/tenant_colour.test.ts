import { describe, expect, it } from "vitest";
import { defaultTenantColour, tenantColour, tenantInitials, TENANT_COLOURS } from "./tenant_colour";

describe("defaultTenantColour", () => {
  it("is the same colour for the same id, from the palette", () => {
    const id = "44444444-4444-4444-4444-444444444444";
    expect(defaultTenantColour(id)).toBe(defaultTenantColour(id));
    expect(TENANT_COLOURS).toContain(defaultTenantColour(id));
  });

  it("spreads ids across the palette rather than collapsing to a few colours", () => {
    const seen = new Set<string>();
    for (let i = 0; i < 200; i++) seen.add(defaultTenantColour(`00000000-0000-4000-8000-${String(i).padStart(12, "0")}`));
    expect(seen.size).toBe(TENANT_COLOURS.length);
  });
});

describe("tenantColour", () => {
  it("prefers the owner's choice over the default", () => {
    expect(tenantColour({ id: "x", colour: "pink" })).toBe("pink");
    expect(tenantColour({ id: "x", colour: null })).toBe(defaultTenantColour("x"));
  });
});

describe("tenantInitials", () => {
  it.each([
    ["Ada's workspace", "AW"],
    ["lab", "L"],
    ["  spaced   out name ", "SO"],
    ["émile zola", "ÉZ"],
    ["   ", "?"],
  ])("%j is %j", (name, initials) => {
    expect(tenantInitials(name)).toBe(initials);
  });
});
