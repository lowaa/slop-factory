// Fixed, not a setting: where a long context starts costing the person
// something, and where it is worth suggesting they clear or compact.
export const CONTEXT_HINT_TOKENS = 150_000;
export const CONTEXT_NEUTRAL_BELOW_TOKENS = 100_000;

export type ContextLevel = "neutral" | "elevated" | "high";

export function contextLevel(tokens: number): ContextLevel {
  if (tokens > CONTEXT_HINT_TOKENS) return "high";
  if (tokens >= CONTEXT_NEUTRAL_BELOW_TOKENS) return "elevated";
  return "neutral";
}

export function formatContextTokens(tokens: number): string {
  return tokens >= 1000 ? `${Math.round(tokens / 1000)}k` : String(tokens);
}
