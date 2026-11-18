const UNITS: readonly { suffix: string; seconds: number }[] = [
  { suffix: "d", seconds: 86400 },
  { suffix: "h", seconds: 3600 },
  { suffix: "m", seconds: 60 },
  { suffix: "s", seconds: 1 },
];

// formatDuration is seconds as the largest unit and the one below it
// ("3d 4h", "12m 5s", "40s"): a report's figures are compared at a glance,
// where "273600s" cannot be, and seconds of precision on a multi-day wait
// is noise. Whole seconds are the finest it shows, so anything under half a
// second reads as "0s" rather than being hidden.
export function formatDuration(seconds: number): string {
  const whole = Math.max(0, Math.round(seconds));
  if (whole === 0) return "0s";
  const counts = UNITS.map((u, i) => Math.floor(whole / u.seconds) % (i === 0 ? Infinity : UNITS[i - 1].seconds / u.seconds));
  const lead = counts.findIndex((n) => n > 0);
  // Only the unit directly below, and only if it is not zero: "1d 5h", but
  // "1d" rather than "1d 0h" or "1d 5s".
  const next = lead + 1 < UNITS.length && counts[lead + 1] > 0 ? ` ${counts[lead + 1]}${UNITS[lead + 1].suffix}` : "";
  return `${counts[lead]}${UNITS[lead].suffix}${next}`;
}
