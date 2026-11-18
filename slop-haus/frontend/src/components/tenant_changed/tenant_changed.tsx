import { useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient } from "@/api";
import { reloadPage } from "@/lib/navigation";
import { useTenantsNow } from "@/lib/shared_resources";
import "./tenant_changed.scss";
import { errorMessage } from "@/lib/use_async_action";

interface TenantChangedProps {
  // The tenant this tab loaded in, which the session has since left.
  was: string;
}

// TenantChanged replaces the whole tab when the session has moved to
// another tenant, from another tab. Everything on screen is the old
// tenant's, and the session now belongs to the new one, so nothing here
// may act: the person chooses which tenant this tab is for. Neither choice
// retries what was refused.
export default function TenantChanged({ was }: TenantChangedProps) {
  // The names are only for the sentence, so a failed listing still leaves
  // both actions.
  const tenants = useTenantsNow();
  const names =
    tenants.status === "loading"
      ? null
      : {
          was: tenants.data?.find((t) => t.id === was) ?? null,
          now: tenants.data?.find((t) => t.current) ?? null,
        };
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function switchBack() {
    setBusy(true);
    setError(null);
    try {
      await apiClient.switchTenant(was);
      reloadPage();
    } catch (err) {
      setBusy(false);
      setError(errorMessage(err, "Failed to switch tenant."));
    }
  }

  const wasName = names?.was?.name ?? "another tenant";
  const nowName = names?.now?.name ?? "another tenant";

  return (
    <div className="tenant-changed" role="alertdialog" aria-labelledby="tenant-changed-title">
      <h1 id="tenant-changed-title">Your session moved to another tenant</h1>
      {names === null ? (
        <p>Loading…</p>
      ) : (
        <p>
          This tab was on <strong>{wasName}</strong>. Your session is now on <strong>{nowName}</strong>, switched in
          another tab. Nothing in this tab was changed.
        </p>
      )}
      <div className="tenant-changed__actions">
        <Button disabled={busy} onClick={reloadPage}>
          {names?.now ? `Reload into ${names.now.name}` : "Reload"}
        </Button>
        <Button disabled={busy} onClick={() => void switchBack()}>
          {busy ? "Switching…" : names?.was ? `Switch back to ${names.was.name}` : "Switch back"}
        </Button>
      </div>
      {error && (
        <p role="alert" className="tenant-changed__error">
          {error}
        </p>
      )}
    </div>
  );
}
