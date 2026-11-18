import { useState, type FormEvent } from "react";
import { apiClient, type Tenant } from "@/api";
import { Button } from "@slop-factory/ui";
import { TenantBadge } from "@/components/tenant_badge";
import {
  defaultTenantColour,
  tenantColour,
  TENANT_COLOUR_LABELS,
  TENANT_COLOURS,
  type TenantColour,
} from "@/lib/tenant_colour";
import { invalidate, resourceKeys } from "@/lib/resource";
import { useCurrentTenant } from "./use_current_tenant";
import { errorMessage } from "@/lib/use_async_action";

// The tenant's colour: picked by its owners from a fixed palette, shown
// read-only to everyone else. "Default" clears the choice, leaving the
// colour derived from the tenant's id.
export default function TenantColourRow() {
  const state = useCurrentTenant("Failed to load this tenant's colour.");

  if (state.status === "loading") return null;
  if (state.status === "error") return null;
  const { tenant } = state;
  if (!tenant.is_owner) {
    return (
      <div className="tenant-settings__item">
        <span className="tenant-settings__name">Colour</span>
        <p className="tenant-settings__value">
          <TenantBadge name={tenant.name} colour={tenantColour(tenant)} compact />
        </p>
        <p className="tenant-settings__meta">Only an owner of the tenant can change its colour.</p>
      </div>
    );
  }
  return <ColourPicker tenant={tenant} />;
}

function ColourPicker({ tenant }: { tenant: Tenant }) {
  const [draft, setDraft] = useState<TenantColour | null>(tenant.colour);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const shown = draft ?? defaultTenantColour(tenant.id);

  async function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (saving || draft === tenant.colour) return;
    setSaving(true);
    setError(null);
    try {
      await apiClient.setTenantColour(draft);
      await invalidate(resourceKeys.tenants());
    } catch (err: unknown) {
      setError(errorMessage(err, "Failed to change the colour."));
    } finally {
      setSaving(false);
    }
  }

  return (
    <form className="tenant-settings__item tenant-colour" onSubmit={(e) => void save(e)}>
      <fieldset className="tenant-colour__choices" disabled={saving}>
        <legend className="tenant-settings__name">Colour</legend>
        <label className="tenant-colour__choice">
          <input type="radio" name="tenant-colour" checked={draft === null} onChange={() => setDraft(null)} />
          <span className="tenant-colour__label">Default</span>
        </label>
        {TENANT_COLOURS.map((key) => (
          <label key={key} className="tenant-colour__choice">
            <input
              type="radio"
              name="tenant-colour"
              data-colour={key}
              checked={draft === key}
              onChange={() => setDraft(key)}
            />
            <span className="tenant-colour__label">{TENANT_COLOUR_LABELS[key]}</span>
          </label>
        ))}
      </fieldset>
      <div className="tenant-colour__preview" aria-label="Preview">
        <span className="tenant-colour__stripe" data-colour={shown} aria-hidden="true" />
        <TenantBadge name={tenant.name} colour={shown} />
      </div>
      <p className="tenant-settings__meta">
        Everyone in the tenant sees this colour in the header, so they can tell it from their other tenants.
        Default picks one from the tenant, and shows it only to people who belong to more than one.
      </p>
      {error && (
        <p role="alert" className="tenant-settings__error">
          {error}
        </p>
      )}
      <div>
        <Button type="submit" variant="primary" disabled={saving || draft === tenant.colour}>
          {saving ? "Saving…" : "Save colour"}
        </Button>
      </div>
    </form>
  );
}
