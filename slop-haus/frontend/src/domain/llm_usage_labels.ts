export function modeLabel(mode: string): string {
  return mode === "subscription" ? "Subscription" : mode === "metered" ? "Metered" : mode;
}

// What a mode's cost means: a subscription's is what the usage would cost at
// API prices, not money spent. Said wherever a cost is shown, so one is never
// read as the other.
export function costKind(mode: string): string {
  return mode === "subscription" ? "estimated" : "spent";
}
