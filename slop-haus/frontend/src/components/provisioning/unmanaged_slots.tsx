import { Button } from "@slop-factory/ui";
import { ApiError, apiClient, type ProvisionSupervisor } from "@/api";
import { errorMessage, useAsyncAction } from "@/lib/use_async_action";
import { usePolledResource } from "@/lib/use_polled_resource";

const POLL_MS = 10_000;
const STALE_MESSAGE = "The agents changed since you looked, so the list was read again. Check it and retry.";

interface Props {
  supervisor: ProvisionSupervisor;
  // The revision of the document the owner is looking at: an import is an edit of it.
  revision: number | undefined;
  onImported: () => Promise<void>;
}

// Slots the supervisor reports that the board's document does not name.
// They are listed whatever their phase: hiding one would hide work the board
// is not managing. Owner-only: the server refuses anyone else.
export default function UnmanagedSlots({ supervisor, revision, onImported }: Props) {
  const list = usePolledResource(() => apiClient.listUnmanagedSlots(supervisor.id), {
    intervalMs: POLL_MS,
    deps: [supervisor.id],
    fallbackError: "Could not load the slots not managed by the board.",
  });
  const action = useAsyncAction();
  const slots = list.data ?? [];

  function importNames(names: string[], failure: string) {
    if (revision === undefined) return Promise.resolve();
    return action.run(async () => {
      try {
        await apiClient.importSupervisorSlots(supervisor.id, revision, names);
      } catch (err) {
        if (err instanceof ApiError && err.status === 409) await Promise.all([onImported(), list.reload()]);
        throw err;
      }
      await Promise.all([onImported(), list.reload()]);
    }, (err) => (err instanceof ApiError && err.status === 409 && /changed since/.test(err.message) ? STALE_MESSAGE : errorMessage(err, failure)));
  }

  return (
    <div className="provisioning__unmanaged">
      {list.error !== null && <p role="alert">{list.error}</p>}
      {action.error !== null && <p role="alert">{action.error}</p>}
      {slots.length > 0 && (
        <>
          <h4>Not managed by the board</h4>
          <ul className="provisioning__agents" aria-label={`Not managed by the board on ${supervisor.name}`}>
            {slots.map((s) => (
              <li key={s.name} className="provisioning__agent">
                <span className="provisioning__name">{s.name}</span>
                <span className="provisioning__kind">{s.kind}</span>
                <span className="provisioning__status">
                  {s.phase}
                  {s.offline && " (offline)"}
                </span>
                <span className="provisioning__actions">
                  <Button size="sm" disabled={action.busy || revision === undefined} onClick={() => importNames([s.name], "Could not import the slot.")}>
                    Import {s.name}
                  </Button>
                </span>
              </li>
            ))}
          </ul>
          <Button
            size="sm"
            disabled={action.busy || revision === undefined}
            onClick={() => importNames(slots.map((s) => s.name), "Could not import the slots.")}
          >
            Import all
          </Button>
        </>
      )}
    </div>
  );
}
