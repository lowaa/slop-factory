// Micro-dollars as dollars: two decimals, or four for a sum under a cent so
// that small usage does not read as free.
export function formatCost(micros: number): string {
  const dollars = micros / 1_000_000;
  return `$${dollars.toFixed(dollars !== 0 && Math.abs(dollars) < 0.01 ? 4 : 2)}`;
}

// Token and request counts, compacted ("1.2M"): the exact figure is not
// what a report is read for.
export function formatCount(n: number): string {
  if (n < 1000) return String(n);
  const units = [
    { at: 1e9, suffix: "B" },
    { at: 1e6, suffix: "M" },
    { at: 1e3, suffix: "k" },
  ];
  const unit = units.find((u) => n >= u.at) ?? units[units.length - 1];
  const value = n / unit.at;
  return `${value >= 100 ? Math.round(value) : value.toFixed(1).replace(/\.0$/, "")}${unit.suffix}`;
}
