import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient, type QueueBlockage } from "@/api";
import TicketLink from "@/shared/ticket_link/ticket_link";
import "./queue_blockage_panel.scss";

interface QueueBlockagePanelProps {
  // Changes whenever the board's own live refresh fires, so this is fetched
  // on the same stream rather than opening a second one.
  refreshToken: number;
}

// Says so on the board when idle agents have work they cannot take only
// because another ticket holds its path scopes: from the board alone an
// idle fleet looks the same as an empty queue. Shown only then -- a busy
// queue waiting its turn on a scope is normal, and a full fleet is not
// blocked, so the server answers nothing for either.
export default function QueueBlockagePanel({ refreshToken }: QueueBlockagePanelProps) {
  const [blockage, setBlockage] = useState<QueueBlockage | null>(null);
  const latest = useRef(0);

  const load = useCallback(() => {
    const request = ++latest.current;
    return apiClient
      .getQueueBlockage()
      .then((found) => {
        if (request === latest.current) setBlockage(found);
      })
      .catch(() => {
        // Quiet: an aside to the board, which a failed read must not
        // replace. The next change tries again.
      });
  }, []);

  useEffect(() => {
    void load();
  }, [load, refreshToken]);

  if (!blockage) return null;
  return (
    <section className="queue-blockage" role="status" aria-label="Queue blocked">
      <h2 className="queue-blockage__title">Queue blocked on a scope</h2>
      <p className="queue-blockage__text">
        Agents are idle, but{" "}
        <TicketLink ticketId={blockage.ticket_id} from="board">
          #{blockage.ticket_id} {blockage.ticket_title}
        </TicketLink>{" "}
        and everything else they could take collides with <code>{blockage.path}</code>, held by{" "}
        <TicketLink ticketId={blockage.holder_ticket_id} from="board">
          #{blockage.holder_ticket_id} {blockage.holder_title}
        </TicketLink>{" "}
        ({blockage.holder_status.replace("_", " ")}
        {blockage.holder_agent_name ? ` by ${blockage.holder_agent_name}` : ""}).{" "}
        {blockage.holder_status === "review"
          ? "It is waiting on a person to approve it."
          : "It frees when that ticket finishes or its lease runs out."}
      </p>
    </section>
  );
}
