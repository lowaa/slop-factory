// Mirrors the server's agent.MaxSlotCap, which refuses anything above it.
export const MAX_SLOT_CAP = 16;
export const DEFAULT_SLOT_CAP = 4;

// parseSlotCap is the cap a typed value stands for, or null when it is not a
// whole number the server would accept.
export function parseSlotCap(value: string): number | null {
  if (!/^\d+$/.test(value)) return null;
  const n = Number(value);
  return n >= 1 && n <= MAX_SLOT_CAP ? n : null;
}
