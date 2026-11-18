import { useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type Tenant } from "@/api";
import { reloadToBoard } from "@/lib/navigation";
import { useTenants } from "@/lib/shared_resources";
import "./tenant_archived.scss";
import { errorMessage } from "@/lib/use_async_action";

// TenantArchived replaces the whole tab when the server says this session's
// tenant was archived (the tenant_archived problem code): everything on
// screen belongs to a tenant nobody can use now, so the person is offered
// the tenants they can still switch to.
export default function TenantArchived() {
  const [switching, setSwitching] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const { status, data } = useTenants();
  const tenants = (data ?? []).filter((t) => !t.current);

  async function switchTo(tenant: Tenant) {
    setSwitching(tenant.id);
    setError(null);
    try {
      await apiClient.switchTenant(tenant.id);
      reloadToBoard();
    } catch (err) {
      setSwitching(null);
      setError(errorMessage(err, "Failed to switch tenant."));
    }
  }

  return (
    <div className="tenant-archived" role="alertdialog" aria-labelledby="tenant-archived-title">
      <h1 id="tenant-archived-title">This tenant was archived</h1>
      <p>Its owner archived it, so it can't be used. Nothing was deleted, and the owner can unarchive it.</p>
      {status === "loading" && <p>Loading…</p>}
      {status === "error" && <p role="alert" className="tenant-archived__error">Failed to load your tenants.</p>}
      {status === "ready" &&
        (tenants.length === 0 ? (
          <p>You have no other tenant to switch to.</p>
        ) : (
          <>
            <p>Switch to another of your tenants:</p>
            <ul className="tenant-archived__tenants">
              {tenants.map((t) => (
                <li key={t.id}>
                  <Button disabled={switching !== null} onClick={() => void switchTo(t)}>
                    {switching === t.id ? "Switching…" : `Switch to ${t.name}`}
                  </Button>
                </li>
              ))}
            </ul>
          </>
        ))}
      {error && (
        <p role="alert" className="tenant-archived__error">
          {error}
        </p>
      )}
    </div>
  );
}
