import { useState, type FormEvent } from "react";
import { apiClient, type Tenant } from "@/api";
import { Button } from "@slop-factory/ui";
import { invalidate, resourceKeys } from "@/lib/resource";
import { useCurrentTenant } from "./use_current_tenant";
import { errorMessage } from "@/lib/use_async_action";

// Keep in step with the server's MaxTenantNameLength (user/tenant_name.go).
const MAX_NAME_LENGTH = 40;

// The tenant's name: edited by its owners, shown to everyone else. A rename
// is recorded in the server's audit log, which this page has no view of, so
// unlike the settings below there is no history here.
export default function TenantNameRow() {
  const state = useCurrentTenant("Failed to load this tenant's name.");

  if (state.status === "loading") return <p className="tenant-settings__muted">Loading…</p>;
  if (state.status === "error") {
    return (
      <p role="alert" className="tenant-settings__error">
        {state.message}
      </p>
    );
  }
  const { tenant } = state;
  if (!tenant.is_owner) {
    return (
      <div className="tenant-settings__item">
        <span className="tenant-settings__name">Name</span>
        <p className="tenant-settings__value">{tenant.name}</p>
        <p className="tenant-settings__meta">Only an owner of the tenant can rename it.</p>
      </div>
    );
  }
  return <EditableName tenant={tenant} />;
}

function EditableName({ tenant }: { tenant: Tenant }) {
  const [draft, setDraft] = useState(tenant.name);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const unchanged = draft.trim() === tenant.name;

  async function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (saving || unchanged) return;
    setSaving(true);
    setError(null);
    try {
      const renamed = await apiClient.renameTenant(draft);
      setDraft(renamed.name);
      await invalidate(resourceKeys.tenants());
    } catch (err: unknown) {
      setError(errorMessage(err, "Failed to rename the tenant."));
    } finally {
      setSaving(false);
    }
  }

  return (
    <form className="tenant-settings__item tenant-settings__name-form" onSubmit={(e) => void save(e)}>
      <label className="tenant-settings__name-field">
        <span className="tenant-settings__name">Name</span>
        <input
          type="text"
          required
          maxLength={MAX_NAME_LENGTH}
          value={draft}
          disabled={saving}
          onChange={(e) => setDraft(e.target.value)}
        />
      </label>
      <p className="tenant-settings__meta">
        How you and the people in it tell this tenant from others. It only has to differ from your other tenants.
      </p>
      {error && (
        <p role="alert" className="tenant-settings__error">
          {error}
        </p>
      )}
      <div>
        <Button type="submit" variant="primary" disabled={saving || unchanged || draft.trim() === ""}>
          {saving ? "Saving…" : "Rename"}
        </Button>
      </div>
    </form>
  );
}
