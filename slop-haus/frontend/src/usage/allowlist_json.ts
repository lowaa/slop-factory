import { CATALOG } from "./catalog.ts";

// The server's allowlist, as it is checked in (see the comment at the top of
// catalog.ts).
export function allowlistJson(): string {
  const events = Object.fromEntries(Object.entries(CATALOG).map(([name, attrs]) => [name, { ...attrs }]));
  return `${JSON.stringify({ events }, null, 2)}\n`;
}
