import { describeLanding } from "./describe_landing";
import { useLandingEntry } from "./landing_queue";

export default function LandingBadge({ ticketId, attemptNumber, className }: { ticketId: number; attemptNumber?: number; className: string }) {
  const entry = useLandingEntry(ticketId, attemptNumber);
  if (!entry) return null;
  const label = describeLanding(entry);
  const tone = label.tone === "back" ? "landing-back" : label.stalled ? "landing-stalled" : "landing";
  return (
    <span className={`${className} ${className}--${tone}`} title={label.stalled ? `${label.title} It has been on this step for longer than the server allows.` : label.title}>
      {label.text}
      {label.since && <span className={`${className}-since`}> · {label.since}</span>}
    </span>
  );
}
