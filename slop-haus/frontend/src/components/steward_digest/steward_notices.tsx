import { Button } from "@slop-factory/ui";
import { apiClient, type StewardNotice } from "@/api";
import { useAsyncAction } from "@/lib/use_async_action";
import { usePolledResource } from "@/lib/use_polled_resource";
import TicketLink from "@/shared/ticket_link/ticket_link";
import "./steward_digest.scss";

const POLL_MS = 30_000;

function minutesUntil(iso: string, now: number): number {
  return Math.max(0, Math.ceil((new Date(iso).getTime() - now) / 60_000));
}

function Subject({ notice }: { notice: StewardNotice }) {
  return notice.ticket_id === null ? (
    <>the workspace</>
  ) : (
    <TicketLink ticketId={notice.ticket_id} from="approvals">
      #{notice.ticket_id}
    </TicketLink>
  );
}

// StewardNotices tells the owner what the steward will do in its veto windows
// and what it did in the last hour, each with the control to take it back.
// It shows nothing while the steward is not live and has nothing to say. Veto
// and undo are the owner's: for anyone else the server refuses and says so
// here.
export default function StewardNotices() {
  const state = usePolledResource(() => apiClient.getStewardNotices(), {
    intervalMs: POLL_MS,
    fallbackError: "Failed to load the steward's notices.",
  });
  const action = useAsyncAction();
  const notices = state.data;
  if (notices === undefined || notices.mode !== "live") return null;
  if (notices.pending.length === 0 && notices.applied.length === 0 && !notices.cap_reached) return null;

  const act = (call: () => Promise<unknown>, failure: string) =>
    action.run(async () => {
      await call();
      await state.reload();
    }, failure);
  const now = Date.now();

  return (
    <section className="steward-notices" aria-label="Steward">
      {notices.cap_reached && (
        <p role="status" className="steward-notices__cap">
          The steward has reached its daily cap of {notices.daily_cap} decisions: further proposals wait for you.
        </p>
      )}
      {notices.pending.length > 0 && (
        <ul className="steward-notices__list" aria-label="The steward will">
          {notices.pending.map((n) => (
            <li key={n.proposal_id} className="steward-notices__item">
              <span>
                The steward will approve the {n.category} proposal on <Subject notice={n} />
                {n.at ? ` in ${minutesUntil(n.at, now)} min` : " shortly"}.
              </span>
              <Button size="sm" disabled={action.busy} onClick={() => void act(() => apiClient.vetoStewardDecision(n.proposal_id), "Failed to veto.")}>
                Veto
              </Button>
            </li>
          ))}
        </ul>
      )}
      {notices.applied.length > 0 && (
        <ul className="steward-notices__list" aria-label="The steward did">
          {notices.applied.map((n) => (
            <li key={n.proposal_id} className="steward-notices__item">
              <span>
                The steward approved the {n.category} proposal on <Subject notice={n} />.
              </span>
              <Button size="sm" disabled={action.busy} onClick={() => void act(() => apiClient.undoStewardDecision(n.proposal_id), "Failed to undo.")}>
                Undo
              </Button>
            </li>
          ))}
        </ul>
      )}
      {action.error && (
        <p role="alert" className="steward-notices__error">
          {action.error}
        </p>
      )}
    </section>
  );
}
