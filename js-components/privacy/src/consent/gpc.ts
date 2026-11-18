import type { PurposeState } from "./types";

// Whether the browser is sending Global Privacy Control. The server reads
// the Sec-GPC header and is authoritative: it denies the sale/share
// purposes and says so per purpose (gpcLocked). The browser's own signal is
// read only to explain that in the UI and to back the server up -- it
// can't tell which purposes are sale/share, so it never decides a purpose
// by itself.
export function readGpcSignal(nav: object | undefined = globalThis.navigator): boolean {
  return (nav as { globalPrivacyControl?: unknown } | undefined)?.globalPrivacyControl === true;
}

// The mapping (go-privacy/README.md §7): GPC denies the purposes flagged
// sale/share, and only those. A purpose the server says GPC is locking
// reads denied here whatever else was reported, so a stale or odd answer
// can never show it granted, or let a script that gates on it load.
export function applyGpc(purpose: PurposeState): PurposeState {
  return purpose.gpcLocked && purpose.granted ? { ...purpose, granted: false } : purpose;
}
