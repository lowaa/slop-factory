import { useState } from "react";
import { Button } from "@slop-factory/ui";
import { apiClient, type AgentMessage } from "@/api";
import TicketLink from "@/shared/ticket_link/ticket_link";
import Timestamp from "@/shared/timestamp/timestamp";
import "./agent_messages.scss";
import { errorMessage } from "@/lib/use_async_action";

// Matches the server's default (agentmsg.DefaultMaxBody), which is what
// actually enforces it: this only saves a round trip.
const MAX_BODY = 4000;
// Enough to see what was said lately and whether it landed; the full
// history is not what this screen is for.
const SHOWN = 5;

interface AgentMessagesProps {
  agentId: string;
  agentName: string;
  // This agent's messages, newest first.
  messages: AgentMessage[];
  // The ticket the agent holds, if any: where direction is better given.
  heldTicketId?: number | null;
  composing: boolean;
  onClose: () => void;
  onSent: () => void;
}

// AgentMessages is the composer for steering one agent, and what became of
// the last few messages. A message is pending until the agent's next call
// carries it, then delivered at a time; there is no "read", because the
// server can only know it handed the text over.
export default function AgentMessages({ agentId, agentName, messages, heldTicketId = null, composing, onClose, onSent }: AgentMessagesProps) {
  const [body, setBody] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function send() {
    setSending(true);
    setError(null);
    try {
      await apiClient.sendAgentMessage(agentId, body);
      setBody("");
      onClose();
      onSent();
    } catch (err) {
      setError(errorMessage(err, "Could not send the message."));
    } finally {
      setSending(false);
    }
  }

  const shown = messages.slice(0, SHOWN);
  if (!composing && shown.length === 0) return null;

  return (
    <div className="agent-messages" role="group" aria-label={`Messages to ${agentName}`}>
      {composing && (
        <div className="agent-messages__composer">
          <label>
            Message to {agentName}
            <textarea
              value={body}
              onChange={(e) => setBody(e.target.value)}
              maxLength={MAX_BODY}
              rows={3}
              disabled={sending}
              placeholder="Delivered on its next slop-haus call or when it ends its turn; a worker holding no ticket gets it at its next session."
            />
          </label>
          <p className="agent-messages__hint">
            For direction on the ticket,{" "}
            {heldTicketId !== null ? (
              <TicketLink ticketId={heldTicketId} from="workers">
                comment on it
              </TicketLink>
            ) : (
              "comment on it"
            )}{" "}
            instead: it stays with the ticket. Use this for instructions about the worker itself.
          </p>
          <div className="agent-messages__actions">
            <Button size="sm" variant="primary" onClick={() => void send()} disabled={sending || body.trim() === ""}>
              {sending ? "Sending…" : "Send"}
            </Button>
            <Button size="sm" variant="ghost" onClick={onClose} disabled={sending}>
              Cancel
            </Button>
          </div>
          {error && (
            <p role="alert" className="agent-messages__error">
              {error}
            </p>
          )}
        </div>
      )}
      {shown.length > 0 && (
        <ul className="agent-messages__list">
          {shown.map((m) => (
            <li key={m.id} className="agent-messages__item">
              {/* Plain text: React escapes it, and pre-wrap keeps the
                  person's own line breaks. */}
              <span className="agent-messages__body">
                {m.kind === "ticket_comment" && m.ticket_id !== null ? `Comment on #${m.ticket_id}: ` : ""}
                {m.body}
              </span>
              <span className={`agent-messages__state agent-messages__state--${m.state}`}>
                {m.state === "delivered" && m.delivered_at ? (
                  <>
                    delivered <Timestamp at={m.delivered_at} />
                  </>
                ) : m.state === "dropped" ? (
                  `not delivered: the agent no longer held #${m.ticket_id}`
                ) : (
                  "pending"
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
