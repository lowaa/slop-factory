import { useState, type FormEvent } from "react";
import { apiClient, ApiError, type Tenant } from "@/api";
import { Button } from "@slop-factory/ui";
import StepUpPrompt from "@/shared/step_up_prompt/step_up_prompt";
import { reloadToBoard } from "@/lib/navigation";
import { useCurrentTenant } from "./use_current_tenant";
import "./archive_tenant_section.scss";
import { errorMessage } from "@/lib/use_async_action";

// What the server does on archive, stated before the owner confirms.
// Keep in step with server/internal/user/tenant_archive.go.
const CONSEQUENCES = [
  "Every agent in the tenant is switched off, and the leases and integration lock they hold are released.",
  "Pending invites to the tenant are revoked.",
  "Members, including you, lose access to the tenant.",
  "Nothing is deleted. You can unarchive the tenant later, from the tenant switcher.",
];

// ArchiveTenantSection is an owner's way to archive the current tenant. It
// renders nothing for anyone else, who cannot do it.
export default function ArchiveTenantSection() {
  const state = useCurrentTenant("Failed to load this tenant.");
  if (state.status !== "loaded" || !state.tenant.is_owner) return null;
  return <ArchiveTenant tenant={state.tenant} />;
}

function ArchiveTenant({ tenant }: { tenant: Tenant }) {
  const [confirming, setConfirming] = useState(false);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [stepUp, setStepUp] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const matches = typed === tenant.name;

  function cancel() {
    setConfirming(false);
    setStepUp(false);
    setTyped("");
    setError(null);
  }

  async function archive() {
    setBusy(true);
    setError(null);
    try {
      await apiClient.archiveTenant(tenant.id, typed);
      reloadToBoard();
    } catch (err: unknown) {
      if (err instanceof ApiError && err.code === "reauth_required") {
        setStepUp(true);
      } else {
        // last_active_tenant's message already says to make another first.
        setError(errorMessage(err, "Failed to archive the tenant."));
      }
    } finally {
      setBusy(false);
    }
  }

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!busy && matches) void archive();
  }

  return (
    <section className="tenant-settings__section" aria-labelledby="archive-tenant-heading">
      <h2 id="archive-tenant-heading">Archive</h2>
      {!confirming ? (
        <div className="tenant-settings__item archive-tenant">
          <p className="tenant-settings__muted">Archiving switches the tenant off and takes it away from everyone in it. Only an owner can.</p>
          <div>
            <Button variant="danger" onClick={() => setConfirming(true)}>
              Archive this tenant…
            </Button>
          </div>
        </div>
      ) : (
        <form className="tenant-settings__item archive-tenant" onSubmit={submit} aria-label={`Archive ${tenant.name}`}>
          <p className="archive-tenant__lead">
            Archive <strong>{tenant.name}</strong>? This is what happens:
          </p>
          <ul className="archive-tenant__consequences">
            {CONSEQUENCES.map((c) => (
              <li key={c}>{c}</li>
            ))}
          </ul>
          <label className="tenant-settings__name-field">
            <span className="tenant-settings__name">Type the tenant's name to confirm</span>
            <input
              type="text"
              value={typed}
              autoComplete="off"
              disabled={busy || stepUp}
              onChange={(e) => setTyped(e.target.value)}
            />
          </label>
          {error && (
            <p role="alert" className="tenant-settings__error">
              {error}
            </p>
          )}
          {!stepUp && (
            <div className="archive-tenant__actions">
              <Button type="submit" variant="danger" disabled={busy || !matches}>
                {busy ? "Archiving…" : "Archive tenant"}
              </Button>
              <Button type="button" onClick={cancel} disabled={busy}>
                Cancel
              </Button>
            </div>
          )}
        </form>
      )}
      {/* Outside the form: its Confirm button would submit it too. */}
      {confirming && stepUp && (
        <StepUpPrompt
          next="/settings"
          onConfirmed={() => {
            setStepUp(false);
            void archive();
          }}
          onCancel={() => setStepUp(false)}
        />
      )}
    </section>
  );
}
