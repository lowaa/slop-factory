import { useEffect, useState, type FormEvent } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type TenantOverview } from "@/api";
import { reloadToBoard } from "@/lib/navigation";
import { MAX_TENANT_NAME_LENGTH } from "@/domain/tenant_names";
import "./new_tenant_page.scss";
import { errorMessage } from "@/lib/use_async_action";

type Load =
  | { status: "loading" }
  | { status: "error" }
  | { status: "ready"; overview: TenantOverview };

// Where a signed-in person starts a tenant of their own, up to a limit the
// server sets. The action stays on the page when the limit is reached, with
// the reason, rather than disappearing from the menu.
export default function NewTenantPage() {
  const [load, setLoad] = useState<Load>({ status: "loading" });
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    apiClient
      .tenantOverview()
      .then((overview) => {
        if (!cancelled) setLoad({ status: "ready", overview });
      })
      .catch(() => {
        if (!cancelled) setLoad({ status: "error" });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  async function create(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (creating) return;
    setCreating(true);
    setError(null);
    try {
      await apiClient.createTenant(name);
      reloadToBoard();
    } catch (err: unknown) {
      setError(errorMessage(err, "Failed to create the tenant."));
      setCreating(false);
    }
  }

  return (
    <div className="new-tenant-page">
      <h2 className="new-tenant-page__title">New tenant</h2>
      {load.status === "loading" && <p className="new-tenant-page__muted">Loading…</p>}
      {load.status === "error" && (
        <p role="alert" className="new-tenant-page__error">
          Failed to load your tenants.
        </p>
      )}
      {load.status === "ready" && (
        <Ready
          overview={load.overview}
          name={name}
          onName={setName}
          creating={creating}
          error={error}
          onSubmit={(e) => void create(e)}
        />
      )}
    </div>
  );
}

interface ReadyProps {
  overview: TenantOverview;
  name: string;
  onName: (name: string) => void;
  creating: boolean;
  error: string | null;
  onSubmit: (e: FormEvent<HTMLFormElement>) => void;
}

function Ready({ overview, name, onName, creating, error, onSubmit }: ReadyProps) {
  const { ownerLimit } = overview;
  const owned = overview.tenants.filter((t) => t.is_owner).length;
  const off = ownerLimit === 0;
  const full = !off && owned >= ownerLimit;

  if (off || full) {
    return (
      <p className="new-tenant-page__muted" role="status">
        {off
          ? "Creating your own tenants is turned off here. Ask an admin to start one for you."
          : `You own ${owned} of ${ownerLimit} tenants, the most you can create yourself. Ask an admin if you need another.`}
      </p>
    );
  }
  return (
    <form className="new-tenant-page__form" onSubmit={onSubmit}>
      <p className="new-tenant-page__muted">
        You own {owned} of {ownerLimit} tenants you can create. Tenants you were invited into don't count.
      </p>
      <label className="new-tenant-page__field">
        <span>Tenant name</span>
        <input
          type="text"
          required
          maxLength={MAX_TENANT_NAME_LENGTH}
          value={name}
          disabled={creating}
          onChange={(e) => onName(e.target.value)}
        />
      </label>
      <small className="new-tenant-page__muted">
        Up to {MAX_TENANT_NAME_LENGTH} characters, different from your other tenants' names.
      </small>
      {error && (
        <p role="alert" className="new-tenant-page__error">
          {error}
        </p>
      )}
      <div>
        <Button type="submit" variant="primary" disabled={creating || name.trim() === ""}>
          {creating ? "Creating…" : "Create tenant"}
        </Button>
      </div>
    </form>
  );
}
