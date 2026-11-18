import { useId } from "react";
import type { DiffLine, Revision, StatusChange } from "@/api";
import { collapseUnchanged, countChanges } from "./diff_rows";
import { AuthorName } from "@/components/author_name";
import Timestamp from "@/shared/timestamp/timestamp";
import StatusHistory from "./status_history";
import "./ticket_history.scss";

interface TicketHistoryProps {
  // Oldest first, as the server sends them.
  revisions: Revision[];
  statusChanges: StatusChange[];
}

const MARKS: Record<DiffLine["op"], string> = { equal: " ", delete: "-", insert: "+" };

export function BodyDiff({ lines }: { lines: DiffLine[] }) {
  return (
    <pre className="ticket-history__diff">
      {collapseUnchanged(lines).map((row, i) =>
        row.kind === "gap" ? (
          <span key={i} className="ticket-history__diff-gap">
            ⋯ {row.hidden} unchanged line{row.hidden === 1 ? "" : "s"}
          </span>
        ) : (
          <span key={i} className={`ticket-history__diff-line ticket-history__diff-line--${row.line.op}`}>
            {MARKS[row.line.op]} {row.line.text}
          </span>
        ),
      )}
    </pre>
  );
}

// TicketHistory lists every edit to a ticket's title and description,
// newest first: who made it, when, and what changed. The server stores the
// full text of each revision and works the description's diff out on read,
// so what is shown here is always derived from two complete texts.
export default function TicketHistory({ revisions, statusChanges }: TicketHistoryProps) {
  const headingId = useId();
  if (revisions.length === 0 && statusChanges.length === 0) return null;
  const newestFirst = revisions.slice().reverse();

  return (
    <section className="ticket-history" aria-labelledby={headingId}>
      <h2 id={headingId}>History</h2>
      <StatusHistory changes={statusChanges} />
      <ol aria-label="Edits">
        {newestFirst.map((r, i) => {
          const changes = r.body_diff && countChanges(r.body_diff);
          return (
            <li key={r.id}>
              <div className="ticket-history__meta">
                <span>Revision {r.revision}</span>
                <AuthorName agentId={r.editor_agent_id} userId={r.editor_user_id} userDeleted={r.editor_user_id_deleted} />
                <Timestamp at={r.created_at} />
              </div>
              {r.old_title !== null && r.new_title !== null && (
                <p className="ticket-history__title">
                  Title: <del>{r.old_title}</del> → <ins>{r.new_title}</ins>
                </p>
              )}
              {r.body_diff && changes && (
                // The newest edit is open: it is the one most likely to be
                // why the human is looking.
                <details className="ticket-history__body" open={i === 0}>
                  <summary>
                    Description changed (+{changes.added} −{changes.removed})
                  </summary>
                  <BodyDiff lines={r.body_diff} />
                </details>
              )}
            </li>
          );
        })}
      </ol>
    </section>
  );
}
