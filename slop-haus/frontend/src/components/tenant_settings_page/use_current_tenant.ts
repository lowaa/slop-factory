import type { Tenant } from "@/api";
import { useTenants } from "@/lib/shared_resources";

export type CurrentTenant =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; tenant: Tenant };

// useCurrentTenant is the tenant the session is in, as the tenant list
// describes it (it carries the owner flag and the colour). It reads the
// shared list, so a write that invalidates the tenants refreshes it, here
// and in the header.
export function useCurrentTenant(failure: string): CurrentTenant {
  const tenants = useTenants({ fallbackError: failure });
  if (tenants.status === "loading") return { status: "loading" };
  if (tenants.status === "error" && !tenants.data) return { status: "error", message: tenants.error ?? failure };
  const tenant = tenants.data?.find((t) => t.current);
  return tenant ? { status: "loaded", tenant } : { status: "error", message: "This tenant isn't in your list." };
}
