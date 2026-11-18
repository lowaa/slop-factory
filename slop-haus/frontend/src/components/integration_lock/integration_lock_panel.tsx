import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type NamedIntegrationLock } from "@/api";
import PrLink from "@/shared/pr_link/pr_link";
import TicketLink from "@/shared/ticket_link/ticket_link";
import Timestamp from "@/shared/timestamp/timestamp";
import { formatDuration } from "@/lib/format_duration";
import { useNow } from "@/lib/use_now";
import { useUsage } from "@/usage/use_usage";
import "./integration_lock_panel.scss";
import { errorMessage } from "@/lib/use_async_action";

// How often the shown durations advance between fetches. The server's
// figures are what the panel starts from; this only keeps them from looking
// frozen on a board nothing is changing on.
const TICK_MS = 30_000;

interface IntegrationLockPanelProps {
  // Changes whenever the board's own live refresh fires, so the lock is
  // fetched on the same stream rather than opening a second one.
  refreshToken: number;
}

type Loaded = { locks: NamedIntegrationLock[]; fetchedAt: number };

const DEFAULT_LOCK = "integration";

// What the integration lock is doing, on the board, so "the merge is
// waiting on CI" can be told from "the holder is dead" without opening a
// ticket -- and the way out for the second: a person releases the lock, or
// removes a queue entry, when the agent's own lease has not yet run out.
//
// One section per lock the tenant has: the default one, and one per repo and
// branch merged under its own base branch. A lock is shown only while someone
// holds or waits for it. An idle lock is the normal case and would be noise on
// every board load.
export default function IntegrationLockPanel({
  refreshToken,
}: IntegrationLockPanelProps) {
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const now = useNow(TICK_MS);
  const [error, setError] = useState<{ lock: string; message: string } | null>(
    null,
  );
  // The one action awaiting its second click: releasing a lock out from
  // under an agent mid-merge is not one to do by a stray click.
  const [confirming, setConfirming] = useState<string | null>(null);
  const latest = useRef(0);

  const load = useCallback(() => {
    const request = ++latest.current;
    return apiClient
      .listIntegrationLocks()
      .then((locks) => {
        if (request !== latest.current) return;
        setLoaded({ locks, fetchedAt: Date.now() });
      })
      .catch(() => {
        // Quiet: the lock is an aside to the board, and a failed read must
        // not replace the board. The next change or tick tries again.
      });
  }, []);

  useEffect(() => {
    void load();
  }, [load, refreshToken]);

  async function act(lock: string, run: () => Promise<void>) {
    setConfirming(null);
    setError(null);
    try {
      await run();
    } catch (err: unknown) {
      setError({
        lock,
        message:
          errorMessage(err, "Could not change the integration lock."),
      });
    }
    await load();
  }

  if (!loaded) return null;
  const { fetchedAt } = loaded;
  const sinceFetch = Math.max(0, (now - fetchedAt) / 1000);

  function confirmable(
    key: string,
    label: string,
    confirmLabel: string,
    lock: string,
    run: () => Promise<void>,
  ) {
    if (confirming !== key) {
      return (
        <Button
          className="integration-lock__action"
          onClick={() => setConfirming(key)}
        >
          {label}
        </Button>
      );
    }
    return (
      <span className="integration-lock__confirm">
        <Button
          className="integration-lock__action integration-lock__action--danger"
          onClick={() => void act(lock, run)}
        >
          {confirmLabel}
        </Button>
        <Button
          className="integration-lock__action"
          onClick={() => setConfirming(null)}
        >
          Cancel
        </Button>
      </span>
    );
  }

  return (
    <>
      {loaded.locks
        .filter((lock) => lock.holder || lock.queue.length > 0)
        .map((lock) => (
          <LockSection
            key={lock.name}
            lock={lock}
            sinceFetch={sinceFetch}
            error={error?.lock === lock.name ? error.message : null}
            confirmable={confirmable}
          />
        ))}
    </>
  );
}

type LockAction = "force_unlock" | "drop_waiter";

// The outcome of the call is counted where it is made; the panel only shows it.
const counted = (usage: ReturnType<typeof useUsage>, what: LockAction, run: () => Promise<void>) => async () => {
  try {
    await run();
    usage.event("lock_action", { action: what, outcome: "done" });
  } catch (err) {
    usage.event("lock_action", { action: what, outcome: "error" });
    throw err;
  }
};

interface LockSectionProps {
  lock: NamedIntegrationLock;
  sinceFetch: number;
  error: string | null;
  confirmable: (
    key: string,
    label: string,
    confirmLabel: string,
    lock: string,
    run: () => Promise<void>,
  ) => ReactNode;
}

function LockSection({
  lock,
  sinceFetch,
  error,
  confirmable,
}: LockSectionProps) {
  const usage = useUsage();
  // The default lock is addressed by no name, as before per-branch locks.
  const name = lock.name === DEFAULT_LOCK ? undefined : lock.name;
  const label = lock.repo
    ? `Integration lock: ${lock.repo} @ ${lock.branch}`
    : "Integration lock";
  const subject = lock.repo
    ? `The integration lock for ${lock.repo} @ ${lock.branch}`
    : "The integration lock";
  const dead = lock.holder?.dead ? lock.holder : null;
  // A dead holder is the louder, more specific problem, so it keeps the slot.
  const stuck = !dead && lock.holder?.long_held ? lock.holder : null;
  const sinceHeartbeat = dead
    ? formatDuration(dead.since_heartbeat_seconds + sinceFetch)
    : "";

  return (
    <>
      {dead && (
        <div role="alert" className="integration-lock-banner">
          {subject} is held by <strong>{dead.agent_name}</strong>, which has
          sent no heartbeat for {sinceHeartbeat}. {lock.queue.length} waiting
          behind it. Release it in the integration lock panel below.
        </div>
      )}
      {stuck && (
        <div role="alert" className="integration-lock-warning">
          {subject} has been held for{" "}
          {formatDuration(stuck.held_seconds + sinceFetch)} by{" "}
          <strong>{stuck.agent_name}</strong> on{" "}
          <TicketLink ticketId={stuck.ticket_id} from="board">
            #{stuck.ticket_id}
          </TicketLink>
          {stuck.pr_url && (
            <>
              {" ("}
              <PrLink
                url={stuck.pr_url}
                ariaLabel={`Pull request of ticket #${stuck.ticket_id} (long hold)`}
              >
                PR
              </PrLink>
              {")"}
            </>
          )}
          ; {lock.queue.length} waiting. Its checks may be stuck: look at the
          PR&apos;s CI.
        </div>
      )}
      <section
        className={`integration-lock${dead ? " integration-lock--dead" : ""}`}
        aria-label={label}
      >
        <h2 className="integration-lock__title">{label}</h2>
        {lock.holder ? (
          <div className="integration-lock__holder">
            {dead && (
              <strong className="integration-lock__dead">
                Dead holder: no heartbeat for {sinceHeartbeat},{" "}
                {lock.queue.length} queued behind it
              </strong>
            )}
            <span>
              Held by <strong>{lock.holder.agent_name}</strong> on{" "}
              <TicketLink ticketId={lock.holder.ticket_id} from="board">
                #{lock.holder.ticket_id} {lock.holder.ticket_title}
              </TicketLink>
            </span>
            <PrLink
              url={lock.holder.pr_url ?? null}
              ariaLabel={`Pull request of ticket #${lock.holder.ticket_id}`}
            >
              PR
            </PrLink>
            <span>
              since <Timestamp at={lock.holder.acquired_at} /> (
              {formatDuration(lock.holder.held_seconds + sinceFetch)})
            </span>
            {confirmable(
              `${lock.name}:release`,
              "Release lock",
              `Release ${lock.holder.agent_name}'s lock`,
              lock.name,
              counted(usage, "force_unlock", () => apiClient.forceUnlockIntegration(name)),
            )}
          </div>
        ) : (
          <p className="integration-lock__free">
            Free, reserved for the first in the queue.
          </p>
        )}
        {lock.queue.length > 0 && (
          <ol
            className="integration-lock__queue"
            aria-label={`Queue for the integration lock${lock.repo ? `: ${lock.repo} @ ${lock.branch}` : ""}`}
          >
            {lock.queue.map((entry) => (
              <li key={entry.agent_id} className="integration-lock__entry">
                <span>
                  <strong>{entry.agent_name}</strong> on{" "}
                  <TicketLink ticketId={entry.ticket_id} from="board">
                    #{entry.ticket_id} {entry.ticket_title}
                  </TicketLink>
                </span>
                <span>
                  waiting {formatDuration(entry.waited_seconds + sinceFetch)}
                </span>
                <span>{entry.priority} priority</span>
                {entry.moved_up_by && (
                  <span>
                    moved up by{" "}
                    {entry.moved_up_by === "aging" ? "waiting" : "priority"}
                  </span>
                )}
                {entry.offered_at && <span>offered the lock</span>}
                {confirmable(
                  `${lock.name}:drop:${entry.agent_id}`,
                  "Remove",
                  `Remove ${entry.agent_name}`,
                  lock.name,
                  counted(usage, "drop_waiter", () =>
                    apiClient.dropIntegrationLockWaiter(entry.agent_id, name),
                  ),
                )}
              </li>
            ))}
          </ol>
        )}
        {error && (
          <p role="alert" className="integration-lock__error">
            {error}
          </p>
        )}
      </section>
    </>
  );
}
