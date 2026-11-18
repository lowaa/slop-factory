// Keep in step with the server's MaxTenantNameLength (user/tenant_name.go).
export const MAX_TENANT_NAME_LENGTH = 40;

const clashes = (name: string, taken: readonly string[]) =>
  taken.some((t) => t.trim().toLowerCase() === name.toLowerCase());

// A starting name for a tenant the person is about to make: their own name,
// or, when they already have a tenant by it, that with a number added. The
// server has the last word; this only saves a refusal.
export function suggestTenantName(
  displayName: string,
  taken: readonly string[],
): string {
  const base = Array.from(displayName.trim())
    .slice(0, MAX_TENANT_NAME_LENGTH)
    .join("");
  if (!clashes(base, taken)) return base;
  for (let n = 2; ; n++) {
    const suffix = ` ${n}`;
    const room = MAX_TENANT_NAME_LENGTH - suffix.length;
    const candidate =
      Array.from(base).slice(0, room).join("").trimEnd() + suffix;
    if (!clashes(candidate, taken)) return candidate;
  }
}
