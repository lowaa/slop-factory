import type { Proposal } from "@/api";
import "./steward_label.scss";

type Steward = NonNullable<Proposal["steward"]>;

// headline is the steward's verdict in words. It is the server's verdict, from
// checks the server ran: the category is only named, never trusted here.
export function stewardHeadline(s: Steward): string {
  switch (s.verdict) {
    case "would_auto":
      return `steward: would auto-approve (${s.category})`;
    case "would_veto":
      return `steward: would approve after ${s.veto_minutes ?? 0} min`;
    default:
      return "steward: needs you";
  }
}

// StewardLabel is shown only when the steward assessed the proposal. The
// reason is the steward's own words, so it is plain text.
export default function StewardLabel({ steward }: { steward: Steward }) {
  return (
    <details className="steward-label">
      <summary>{stewardHeadline(steward)}</summary>
      {steward.reason !== "" && <p className="steward-label__reason">{steward.reason}</p>}
      <ul className="steward-label__evidence">
        {steward.evidence.map((e, i) => (
          <li key={`${e.check}-${i}`} className={e.ok ? "steward-label__ok" : "steward-label__failed"}>
            {e.ok ? "✓" : "✗"} {e.check}
            {e.detail ? `: ${e.detail}` : ""}
            {e.ref ? ` (${e.ref})` : ""}
          </li>
        ))}
      </ul>
    </details>
  );
}
