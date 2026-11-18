import { useEffect, useState } from "react";
import { apiClient, type LlmUsageSums, type TicketLlmUsage } from "@/api";
import { formatCount } from "@/lib/format_usage";
import { Cost } from "@/components/reports_panel";
import { modeLabel } from "@/domain/llm_usage_labels";
import "./ticket_usage.scss";

interface TicketUsageProps {
  ticketId: number;
}

// Worker and guidance usage are different spend (what the ticket's agents
// ran versus what a console or conductor declared it spent steering it), so
// they are separate rows and never added together; nor are the billing modes.
const KINDS = [
  { key: "worker", label: "Worker" },
  { key: "guidance", label: "Guidance" },
] as const;

function Figures({ sums }: { sums: LlmUsageSums }) {
  const tokens = sums.input_tokens + sums.output_tokens + sums.cache_read_tokens + sums.cache_write_tokens;
  return (
    <li className="ticket-usage__row" data-mode={sums.billing_mode}>
      <span className="ticket-usage__mode">{modeLabel(sums.billing_mode)}</span>
      <span>{formatCount(tokens)} tokens</span>
      <span>{formatCount(sums.requests)} requests</span>
      <Cost sums={sums} detail />
    </li>
  );
}

// Shown only once the ticket has usage: it is context for a ticket that cost
// something, and a failed read is not worth an error on a page that works
// without it.
export default function TicketUsage({ ticketId }: TicketUsageProps) {
  const [usage, setUsage] = useState<{ ticketId: number; usage: TicketLlmUsage } | null>(null);
  useEffect(() => {
    let cancelled = false;
    apiClient
      .getTicketLlmUsage(ticketId)
      .then((u) => {
        if (!cancelled) setUsage({ ticketId, usage: u });
      })
      // Quiet: usage is an aside to the ticket; the section just stays empty.
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [ticketId]);

  // Another ticket's figures are never shown while this one's load.
  const current = usage?.ticketId === ticketId ? usage.usage : null;
  if (!current || KINDS.every(({ key }) => current[key].length === 0)) return null;
  return (
    <section className="ticket-usage llm-usage" aria-label="LLM usage">
      <h2>LLM usage</h2>
      {KINDS.map(({ key, label }) =>
        current[key].length === 0 ? null : (
          <div key={key} className="ticket-usage__kind" data-kind={key}>
            <h3>{label}</h3>
            <ul>
              {current[key].map((sums) => (
                <Figures key={sums.billing_mode} sums={sums} />
              ))}
            </ul>
          </div>
        ),
      )}
    </section>
  );
}
