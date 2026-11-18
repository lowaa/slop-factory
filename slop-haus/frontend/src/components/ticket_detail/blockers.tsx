import type { Ticket } from "@/api";
import { FilterableDropdown } from "@slop-factory/ui";
import type { BlockerActions } from "./use_blockers";
import "./blockers.scss";

interface BlockersProps {
  ticket: Ticket;
  blockers: BlockerActions;
}

export default function Blockers({ ticket, blockers }: BlockersProps) {
  const { action, candidates } = blockers;
  return (
    <section>
      <h2>Blocked by</h2>
      {ticket.blockers.length > 0 ? (
        <ul className="ticket-detail__depends">
          {ticket.blockers.map(({ id, title, status }) => (
            <li key={id}>
              <span>
                #{id} {title}
                {/* Parked is the one blocker nobody is working on,
                    so it is the one that leaves a ticket stuck
                    with no visible reason. */}
                {status === "parked" && <span className="ticket-detail__blocker-parked">, parked</span>}
              </span>
              <button type="button" disabled={action.busy} onClick={() => blockers.handleRemoveBlocker(id)}>
                Remove
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <p className="ticket-detail__empty">Nothing is blocking this ticket.</p>
      )}

      {/* A picker rather than a typed id: the server already knows
          which tickets would close a cycle, and making a human
          discover that by being refused is a worse way to learn it.
          The refusal path below stays regardless -- this list is a
          snapshot, and another writer can invalidate it between the
          fetch and the click. */}
      {candidates.status === "loading" && <p className="ticket-detail__empty">Loading tickets…</p>}

      {candidates.status === "error" && (
        <p className="ticket-detail__empty">
          Couldn't load the tickets this one can block on.{" "}
          <button type="button" className="ticket-detail__retry" onClick={blockers.refreshCandidates}>
            Try again
          </button>
        </p>
      )}

      {candidates.status === "loaded" &&
        (candidates.tickets.length > 0 ? (
          // Filterable rather than plain: this list is as long as the
          // board, and finding #34 by scrolling past thirty others is
          // not picking, it is searching with the wrong tool.
          <form className="ticket-detail__add-blocker" onSubmit={blockers.handleAddBlocker}>
            <FilterableDropdown
              label="Add blocker"
              value={blockers.blockerIdInput === "" ? null : blockers.blockerIdInput}
              options={candidates.tickets.map((c) => ({
                value: String(c.id),
                label: `#${c.id} ${c.title}`,
                hint: c.status,
              }))}
              onChange={blockers.setBlockerIdInput}
              placeholder="Choose a ticket…"
            />
            <button type="submit" disabled={action.busy}>
              {action.busy ? "Saving…" : "Add"}
            </button>
          </form>
        ) : (
          <p className="ticket-detail__empty">No other ticket can block this one without creating a cycle.</p>
        ))}

      {action.error && (
        <p role="alert" className="ticket-detail__notice">
          {action.error}
        </p>
      )}
    </section>
  );
}
