import { useState } from "react";
import { apiClient, ApiError, type Tenant } from "@/api";
import { Button, Dropdown } from "@slop-factory/ui";
import StepUpPrompt from "@/shared/step_up_prompt/step_up_prompt";
import { reloadToBoard } from "@/lib/navigation";
import { invalidate, resourceKeys } from "@/lib/resource";
import { useArchivedTenants, useTenants } from "@/lib/shared_resources";
import { tenantColour, tenantInitials } from "@/lib/tenant_colour";
import "./tenant_switcher.scss";
import { errorMessage } from "@/lib/use_async_action";

// What the person's switch is doing, once they have chosen a tenant.
type Switch = { status: "switching" } | { status: "error"; message: string };

// Two tenants can share a name's first letters and even an owner's name, so
// each entry carries its colour badge too (initials drawn by CSS so the
// combobox's text stays just the tenant's name); the owner is a hint, not part of
// the label, so the trigger stays as short as the header's own badge.
function toOption(t: Tenant) {
  const owners = t.owners ?? [];
  return {
    value: t.id,
    label: t.name,
    hint: owners.length > 0 ? `${owners.length > 1 ? "owners" : "owner"}: ${owners.join(", ")}` : undefined,
    leading: <span className="tenant-switcher__swatch" data-colour={tenantColour(t)} data-initials={tenantInitials(t.name)} />,
  };
}

// TenantSwitcher only renders once it does something: the signed-in user
// belongs to more than one tenant, or has archived one to bring back. The
// common case is exactly one, and a control that always exists just to be
// disabled or empty is worse.
// labelHidden keeps the "Tenant" label for assistive tech only, where the
// header's one row has no room for a label stacked over the control.
export default function TenantSwitcher({ labelHidden = false }: { labelHidden?: boolean }) {
  const tenantsResource = useTenants();
  const archivedResource = useArchivedTenants();
  const [attempt, setAttempt] = useState<Switch | null>(null);
  const [showArchived, setShowArchived] = useState(false);
  const [unarchiving, setUnarchiving] = useState<string | null>(null);
  const [stepUpFor, setStepUpFor] = useState<string | null>(null);
  const [archivedError, setArchivedError] = useState<string | null>(null);

  // Only an owner has archived tenants to list; a failure there should not
  // hide the tenants they can switch between.
  const tenants = tenantsResource.data ?? [];
  const archived = archivedResource.data ?? [];
  const settled = tenantsResource.status !== "loading" && archivedResource.status !== "loading";
  // Hidden covers both a fetch failure and the (common) case of exactly one
  // tenant -- either way there is nothing useful to switch between, and a
  // picker with one option or an error message in the header is worse than
  // no picker at all.
  if (!settled || tenantsResource.status === "error" || (tenants.length <= 1 && archived.length === 0)) return null;

  const current = tenants.find((t) => t.current) ?? null;

  async function handleChange(tenantId: string) {
    if (tenantId === current?.id) return;
    setAttempt({ status: "switching" });
    try {
      await apiClient.switchTenant(tenantId);
      reloadToBoard();
    } catch (err) {
      setAttempt({ status: "error", message: errorMessage(err, "Failed to switch tenant.") });
    }
  }

  async function unarchive(tenantId: string) {
    setUnarchiving(tenantId);
    setArchivedError(null);
    try {
      await apiClient.unarchiveTenant(tenantId);
      setStepUpFor(null);
      // ["tenants"] covers the archived list too.
      await invalidate(resourceKeys.tenants());
    } catch (err) {
      if (err instanceof ApiError && err.code === "reauth_required") {
        setStepUpFor(tenantId);
      } else {
        // tenant_limit_reached says they own as many active tenants as
        // allowed, and to archive one first.
        setArchivedError(errorMessage(err, "Failed to unarchive the tenant."));
      }
    } finally {
      setUnarchiving(null);
    }
  }

  return (
    <div className="tenant-switcher">
      <Dropdown
        label="Tenant"
        labelHidden={labelHidden}
        value={current?.id ?? null}
        options={tenants.map(toOption)}
        onChange={(value) => void handleChange(value)}
        disabled={attempt?.status === "switching"}
      />
      {attempt?.status === "error" && (
        <p className="tenant-switcher__notice" role="alert">
          {attempt.message}
        </p>
      )}
      {archived.length > 0 && (
        <>
          <button
            type="button"
            className="tenant-switcher__archived-toggle"
            aria-expanded={showArchived}
            onClick={() => setShowArchived((open) => !open)}
          >
            {showArchived ? "Hide" : "Show"} archived ({archived.length})
          </button>
          {showArchived && (
            <div className="tenant-switcher__archived" role="group" aria-label="Archived tenants">
              <ul>
                {archived.map((t) => (
                  <li key={t.id}>
                    <span className="tenant-switcher__swatch" data-colour={tenantColour(t)} data-initials={tenantInitials(t.name)} />
                    <span className="tenant-switcher__archived-name">{t.name}</span>
                    <Button size="sm" disabled={unarchiving !== null} onClick={() => void unarchive(t.id)} aria-label={`Unarchive ${t.name}`}>
                      Unarchive
                    </Button>
                  </li>
                ))}
              </ul>
              {archivedError && (
                <p className="tenant-switcher__notice" role="alert">
                  {archivedError}
                </p>
              )}
              {stepUpFor && (
                <StepUpPrompt next="/" onConfirmed={() => void unarchive(stepUpFor)} onCancel={() => setStepUpFor(null)} />
              )}
            </div>
          )}
        </>
      )}
    </div>
  );
}
