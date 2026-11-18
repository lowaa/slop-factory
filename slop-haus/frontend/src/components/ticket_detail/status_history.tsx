import { useId } from "react";
import { Link } from "@slop-factory/router";
import type { StatusChange } from "@/api";
import { AuthorName } from "@/components/author_name";
import Timestamp from "@/shared/timestamp/timestamp";
import "./status_history.scss";

function statusLabel(status: string): string {
  return status.replace("_", " ");
}

// A move by a person the route did not name has neither an id nor the
// deleted flag; AuthorName would call that the system, which is untrue.
function Actor({ change }: { change: StatusChange }) {
  if (change.actor_kind === "user" && !change.actor_id && !change.actor_id_deleted) {
    return <span className="author-name--notice">A person</span>;
  }
  return (
    <AuthorName
      agentId={change.actor_kind === "agent" ? change.actor_id : null}
      userId={change.actor_kind === "user" ? change.actor_id : null}
      userDeleted={change.actor_id_deleted}
    />
  );
}

function Cause({ cause }: { cause: NonNullable<StatusChange["cause"]> }) {
  if (cause.kind === "proposal") {
    // Decided proposals are listed on the Approvals tab's Decided view,
    // which has no address of its own per proposal.
    return (
      <span className="status-history__cause">
        via a <Link to="/approvals">proposal</Link>
        {cause.auto_approved && (
          <>
            {" "}
            (<span className="status-history__auto">auto-approved</span>)
          </>
        )}
      </span>
    );
  }
  return <span className="status-history__cause">{cause.text}</span>;
}

// StatusHistory lists every move of the ticket's status, newest first like
// the edits above it: from and to, who moved it, when, and why. A list of its
// own rather than interleaved with the edits: a move is one line and an edit
// a diff, and the two are loaded and refreshed separately.
export default function StatusHistory({ changes }: { changes: StatusChange[] }) {
  const headingId = useId();
  if (changes.length === 0) return null;
  const newestFirst = changes.slice().reverse();

  return (
    <section className="status-history" aria-labelledby={headingId}>
      <h3 id={headingId}>Status changes</h3>
      <ol>
        {newestFirst.map((c) => (
          <li key={c.id}>
            <span className="status-history__move">
              {c.from_status === null ? "Created as" : `${statusLabel(c.from_status)} →`} <strong>{statusLabel(c.to_status)}</strong>
            </span>
            <span className="status-history__meta">
              <Actor change={c} />
              <Timestamp at={c.changed_at} />
              {c.cause && <Cause cause={c.cause} />}
            </span>
          </li>
        ))}
      </ol>
    </section>
  );
}
