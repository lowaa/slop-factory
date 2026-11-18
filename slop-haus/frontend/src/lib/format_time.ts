export const MINUTE_MS = 60 * 1000;
export const DAY_MS = 24 * 60 * MINUTE_MS;

// expiresIn labels a deadline as "expires in 5m" / "expires in 3d", or
// "expired" once it has passed. The count rounds to the nearest whole unit
// but never reads 0, so a deadline that is still ahead is not shown as gone.
export function expiresIn(iso: string, now: number, unit: "minute" | "day"): string {
  const diffMs = new Date(iso).getTime() - now;
  if (diffMs <= 0) return "expired";
  const unitMs = unit === "day" ? DAY_MS : MINUTE_MS;
  const count = Math.max(1, Math.round(diffMs / unitMs));
  return `expires in ${count}${unit === "day" ? "d" : "m"}`;
}
