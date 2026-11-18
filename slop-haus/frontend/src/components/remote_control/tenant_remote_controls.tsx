import { useMemo, useState } from "react";
import { apiClient, type ConductorDirectoryEntry } from "@/api";
import { AgentAuthorizations, grantsOf, useGrants, type GrantsHandle } from "@/components/agent_authorizations";
import { AgentLifecycleActions, AgentLifecycleFeedback, AgentStateBadge, ShowRemovedToggle, useConductorLifecycle } from "@/components/agent_lifecycle";
import { useTenants } from "@/lib/shared_resources";
import { usePolledResource } from "@/lib/use_polled_resource";
import HelpSection from "./help_section";
import "./tenant_remote_controls.scss";

const REFRESH_MS = 20_000;

export const TENANT_REMOTE_CONTROLS_TITLE = "Remote controls in this tenant";

// TenantRemoteControls lists every remote control of the tenant, the person's
// own included, for any member. It is built from the directory, which carries
// no sessions' titles or content: only whether one is live.
export default function TenantRemoteControls({ asView = false }: { asView?: boolean }) {
  const directory = usePolledResource(() => apiClient.conductorDirectory({ includeArchived: true }), {
    intervalMs: REFRESH_MS,
    fallbackError: "Could not load the tenant's remote controls.",
  });
  // The directory names owners, not who is asking; the person's own are the
  // ones their own list returns. The server checks every action regardless.
  const own = usePolledResource(() => apiClient.listConductors({ includeArchived: true }), {
    fallbackError: "Could not load your remote controls.",
  });
  const tenants = useTenants();
  const isTenantOwner = tenants.data?.find((t) => t.current)?.is_owner === true;
  const ownIds = useMemo(() => new Set((own.data?.own ?? []).filter((c) => c.owned_by_me).map((c) => c.id)), [own.data]);
  const [showRemoved, setShowRemoved] = useState(false);
  const grantsHandle = useGrants();

  const entries = directory.data ?? [];
  const live = entries.filter((e) => e.archived_at === null);
  const removed = entries.filter((e) => e.archived_at !== null);
  const reload = () => void directory.reload();

  return (
    <HelpSection className="tenant-remote-controls" title={TENANT_REMOTE_CONTROLS_TITLE} asView={asView}>
      <div className="tenant-remote-controls__body">
        {directory.status === "loading" && <p>Loading…</p>}
        {directory.status === "error" && <p role="alert">{directory.error}</p>}
        {directory.status !== "loading" && directory.data && live.length === 0 && removed.length === 0 && (
          <p className="tenant-remote-controls__empty">No one in this tenant has a remote control yet.</p>
        )}
        {live.length > 0 && (
          <ul className="tenant-remote-controls__list" aria-label="Remote controls">
            {live.map((e) => (
              <Row key={e.id} entry={e} canManage={isTenantOwner || ownIds.has(e.id)} mine={ownIds.has(e.id)} grantsHandle={grantsHandle} onChanged={reload} />
            ))}
          </ul>
        )}
        <ShowRemovedToggle count={removed.length} checked={showRemoved} onChange={setShowRemoved} />
        {showRemoved && removed.length > 0 && (
          <ul className="tenant-remote-controls__list" aria-label="Removed remote controls">
            {removed.map((e) => (
              <Row key={e.id} entry={e} canManage={isTenantOwner || ownIds.has(e.id)} mine={ownIds.has(e.id)} grantsHandle={grantsHandle} onChanged={reload} />
            ))}
          </ul>
        )}
      </div>
    </HelpSection>
  );
}

function lastConnected(entry: ConductorDirectoryEntry): string {
  return entry.host_seen_at ? `Last connected ${new Date(entry.host_seen_at).toLocaleString()}` : "Never connected";
}

interface RowProps {
  entry: ConductorDirectoryEntry;
  canManage: boolean;
  mine: boolean;
  grantsHandle: GrantsHandle;
  onChanged: () => void;
}

function Row({ entry, canManage, mine, grantsHandle, onChanged }: RowProps) {
  // One a supervisor runs is removed through that supervisor, which only its owner (the approver) can do from here.
  const runBy = entry.run_by_supervisor_id;
  const lifecycle = useConductorLifecycle(entry, () => onChanged(), mine ? runBy : undefined);
  const owner = entry.owner_display_name ?? "A deleted account";
  return (
    <li className="tenant-remote-controls__row">
      <div className="tenant-remote-controls__line">
        <span className="tenant-remote-controls__name">{entry.name}</span>
        <span className="tenant-remote-controls__owner">{mine ? `${owner} (you)` : owner}</span>
        <AgentStateBadge agent={entry} />
        <span className="tenant-remote-controls__seen">{lastConnected(entry)}</span>
        {entry.has_live_session && <span className="tenant-remote-controls__live">Live session</span>}
        {canManage && <AgentLifecycleActions lifecycle={lifecycle} removable={!runBy || (mine && entry.archived_at === null)} />}
      </div>
      {canManage && <AgentLifecycleFeedback lifecycle={lifecycle} />}
      {/* Revoking a remote control's sign-ins lives here since the workers page does not list remote controls. */}
      {canManage && entry.archived_at === null && <AgentAuthorizations grants={grantsOf(grantsHandle, entry.id)} handle={grantsHandle} />}
    </li>
  );
}
