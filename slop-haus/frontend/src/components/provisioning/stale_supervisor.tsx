import { Button } from "@slop-factory/ui";
import { apiClient, type ProvisionSupervisor } from "@/api";
import { useAsyncAction } from "@/lib/use_async_action";

interface Props {
  supervisor: ProvisionSupervisor;
  // Only the tenant's owner is offered the action.
  canEnd: boolean;
  onEnded: () => Promise<void>;
}

// Offered when the server calls a supervisor stale. Ending it revokes its
// credentials, so a machine that is only offline for a while would have to be
// set up again: the person decides, nothing ends on its own.
export default function StaleSupervisor({ supervisor, canEnd, onEnded }: Props) {
  const action = useAsyncAction();
  if (!supervisor.stale) return null;

  async function end() {
    if (!window.confirm(`End ${supervisor.name}? Its workers stop and its credentials are revoked; the machine would need setting up again.`)) return;
    await action.run(async () => {
      await apiClient.setSupervisorDisabled(supervisor.id, true);
      await onEnded();
    }, "Could not end the supervisor.");
  }

  return (
    <div className="provisioning__stale">
      <p role="note">Not heard from since {new Date(supervisor.last_seen_at).toLocaleString()}. Its machine may be gone.</p>
      {action.error !== null && <p role="alert">{action.error}</p>}
      {canEnd && (
        <Button size="sm" variant="danger" disabled={action.busy} onClick={end}>
          End supervisor {supervisor.name}
        </Button>
      )}
    </div>
  );
}
