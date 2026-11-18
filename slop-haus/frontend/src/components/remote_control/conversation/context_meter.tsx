import { contextLevel, formatContextTokens } from "@/lib/context_level";
import "./context_meter.scss";

// ContextMeter shows how much context the session carries. Nothing for 0,
// which is unknown.
export default function ContextMeter({ tokens }: { tokens: number }) {
  if (!(tokens > 0)) return null;
  const level = contextLevel(tokens);
  return (
    <span className={`context-meter context-meter--${level}`} role="status" aria-label="Context size">
      Context: {formatContextTokens(tokens)}
      {level === "high" && <span className="context-meter__hint"> · Long: consider Clear or Compact</span>}
    </span>
  );
}
