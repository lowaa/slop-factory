import { useState } from "react";
import { Button } from "@slop-factory/ui";
import type { AgentContext, Comment, TicketAttempt } from "@/api";
import AgentContextHistory from "./agent_context_history";
import { CommentList } from "./comments";
import { commentsOfAttempt, contextsOfAttempt } from "./attempt_view";
import "./blind_conversations.scss";

interface BlindConversationsProps {
  siblings: TicketAttempt[];
  comments: Comment[];
  contexts: AgentContext[];
}

// What was said to the other attempts. It starts out of sight, and is not
// rendered at all until asked for: what you told one attempt should not
// shape what you tell another, unless you choose to look. A default, not a
// rule, so the control is always there.
//
// The state is this component's, and the page of an attempt is keyed by the
// attempt, so moving to another attempt starts blind again.
export default function BlindConversations({ siblings, comments, contexts }: BlindConversationsProps) {
  const [expanded, setExpanded] = useState(false);
  if (siblings.length === 0) return null;
  const said = siblings.reduce(
    (n, a) => n + commentsOfAttempt(comments, a.number).length + contextsOfAttempt(contexts, a.number).length,
    0,
  );
  return (
    <section className="blind-conversations" aria-label="Other attempts' conversations">
      <div className="blind-conversations__toolbar">
        <p>
          {expanded
            ? "Showing the other attempts' conversations."
            : `The other attempts' conversations are hidden so they do not anchor what you tell this one (${said} message${said === 1 ? "" : "s"}).`}
        </p>
        <Button aria-expanded={expanded} onClick={() => setExpanded((prev) => !prev)}>
          {expanded ? "Hide other attempts' conversations" : "Show other attempts' conversations"}
        </Button>
      </div>
      {expanded &&
        siblings.map((a) => (
          <section key={a.number} className="blind-conversations__attempt" aria-label={`Attempt ${a.number} conversation`}>
            <h3>Attempt {a.number}</h3>
            <CommentList comments={commentsOfAttempt(comments, a.number)} />
            <AgentContextHistory contexts={contextsOfAttempt(contexts, a.number)} />
          </section>
        ))}
    </section>
  );
}
