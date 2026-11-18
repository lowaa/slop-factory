import { useState } from "react";
import { Button } from "@slop-factory/ui";
import type { AgentContext } from "@/api";
import AgentName from "@/shared/names/agent_name";
import RichText from "@/shared/rich_text/rich_text";
import Timestamp from "@/shared/timestamp/timestamp";
import "./agent_context_history.scss";

interface AgentContextHistoryProps {
  // Oldest first, as the server sends them.
  contexts: AgentContext[];
}

function firstLine(body: string): string {
  return body.trimStart().split("\n", 1)[0];
}

// AgentContextHistory is every handoff an agent has left on a ticket,
// newest first. A handoff is written for an agent picking the work up
// cold, so each one runs long; they are folded to one line each, with the
// newest open, since that is the one the next agent will start from.
export default function AgentContextHistory({ contexts }: AgentContextHistoryProps) {
  const latestId = contexts.length > 0 ? contexts[contexts.length - 1].id : null;
  // Which entries are open is state here rather than left to each
  // <details>: "Expand all" has to be able to set every one of them.
  const [openIds, setOpenIds] = useState<ReadonlySet<number>>(() => new Set(latestId === null ? [] : [latestId]));
  // A handoff that lands while this is on screen (the refetch after the
  // human answers a discussion) is the new latest, and opens like the
  // first one did. Adjusted during render, as ticket_detail.tsx does for a
  // change of ticket, so it never paints closed first.
  const [openedLatest, setOpenedLatest] = useState(latestId);
  if (latestId !== openedLatest) {
    setOpenedLatest(latestId);
    if (latestId !== null) setOpenIds((prev) => new Set(prev).add(latestId));
  }

  if (contexts.length === 0) {
    return <p className="agent-context-history__empty">No agent has left a handoff on this ticket yet.</p>;
  }

  const newestFirst = contexts.slice().reverse();
  const allOpen = contexts.every((c) => openIds.has(c.id));

  function setOpen(id: number, open: boolean) {
    setOpenIds((prev) => {
      if (prev.has(id) === open) return prev;
      const next = new Set(prev);
      if (open) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  return (
    <section className="agent-context-history">
      <div className="agent-context-history__toolbar">
        <p>
          {contexts.length} handoff{contexts.length === 1 ? "" : "s"}, newest first.
        </p>
        <Button onClick={() => setOpenIds(allOpen ? new Set() : new Set(contexts.map((c) => c.id)))}>
          {allOpen ? "Collapse all" : "Expand all"}
        </Button>
      </div>
      <ol>
        {newestFirst.map((c) => (
          <li key={c.id}>
            <details
              className={`agent-context-history__entry${c.id === latestId ? " agent-context-history__entry--latest" : ""}`}
              open={openIds.has(c.id)}
              // The browser toggles <details> itself before this fires;
              // following it keeps the set true to what is on screen.
              onToggle={(e) => setOpen(c.id, e.currentTarget.open)}
            >
              <summary>
                <span className="agent-context-history__meta">
                  <Timestamp at={c.created_at} />
                  <AgentName id={c.agent_id} />
                  {c.id === latestId && <span className="agent-context-history__badge">latest</span>}
                </span>
                <span className="agent-context-history__preview">{firstLine(c.body)}</span>
              </summary>
              <RichText className="agent-context-history__body" text={c.body} />
            </details>
          </li>
        ))}
      </ol>
    </section>
  );
}
