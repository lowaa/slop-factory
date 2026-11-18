import { useState, type ReactNode } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type Tenant } from "@/api";
import { reloadPage } from "@/lib/navigation";
import { useTenants } from "@/lib/shared_resources";
import "./tenant_mismatch.scss";
import { errorMessage } from "@/lib/use_async_action";

interface TenantMismatchProps {
  ticketId: number;
  // The tenant the link names, which is not the session's.
  tenantId: string;
  // The way back to a tab, for staying where the session is.
  back: ReactNode;
}

// What the person's switch attempt is doing, once they have made one.
type Switch = { status: "switching" } | { status: "switch_failed"; message: string };

// TenantMismatch is shown in place of a ticket whose link names a tenant
// other than the session's. Ticket numbers are per tenant, so opening the
// number here would silently show a different ticket; switching first is
// the person's call, never the link's.
export default function TenantMismatch({ ticketId, tenantId, back }: TenantMismatchProps) {
  const tenants = useTenants({ fallbackError: "Failed to load your tenants." });
  const [attempt, setAttempt] = useState<Switch | null>(null);

  const target = tenants.data?.find((t) => t.id === tenantId) ?? null;
  const current = tenants.data?.find((t) => t.current) ?? null;

  async function switchTenant(target: Tenant) {
    setAttempt({ status: "switching" });
    try {
      await apiClient.switchTenant(target.id);
      // The URL still names this ticket and its tenant, now the session's:
      // a reload shows it, with every list fetched afresh for the tenant.
      reloadPage();
    } catch (err) {
      setAttempt({ status: "switch_failed", message: errorMessage(err, "Failed to switch tenant.") });
    }
  }

  return (
    <div className="tenant-mismatch">
      {back}
      <h1>Ticket #{ticketId} is in another tenant</h1>

      {tenants.status === "loading" && <p>Loading…</p>}
      {tenants.status === "error" && (
        <p role="alert" className="tenant-mismatch__notice">
          {tenants.error}
        </p>
      )}
      {tenants.status === "ready" && !target && (
        // The link's tenant is not one the user belongs to: nothing to switch to.
        <p>This link is to a ticket in a tenant you are not a member of, so it can&rsquo;t be opened here.</p>
      )}

      {tenants.status === "ready" && target && (
        <>
          <p>
            This link is to ticket #{ticketId} in <strong>{target.name}</strong>, but you are working in{" "}
            <strong>{current?.name ?? "another tenant"}</strong>. Ticket numbers are per tenant, so #{ticketId}{" "}
            here would be a different ticket.
          </p>
          <div className="tenant-mismatch__actions">
            <Button disabled={attempt?.status === "switching"} onClick={() => void switchTenant(target)}>
              {attempt?.status === "switching" ? "Switching…" : `Switch to ${target.name}`}
            </Button>
          </div>
          {attempt?.status === "switch_failed" && (
            <p role="alert" className="tenant-mismatch__notice">
              {attempt.message}
            </p>
          )}
        </>
      )}
    </div>
  );
}
