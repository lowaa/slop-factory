import type { BrowserContext } from "@playwright/test";

import { BOARD_ORIGIN } from "@/support/env";

interface ConsentPurpose {
  id: string;
  kind: string;
  necessary: boolean;
  document_version?: string;
  based_on: number;
}

// How a test's browser starts. The consent banner is fixed to the bottom of
// the page and would cover whatever a spec clicks there, so by default the
// browser has already refused every optional purpose, as a visitor who
// clicked "Reject all" would have. A spec about consent itself says "none".
export type ConsentStart = "reject" | "none";

// rejectAllConsent decides through the server's own route, as the banner's
// button does, so the cookie the context holds is the one the server set.
export async function rejectAllConsent(context: BrowserContext): Promise<void> {
  const state = await context.request.get("/api/privacy/consent");
  if (!state.ok()) throw new Error(`consent state: ${state.status()} ${await state.text()}`);
  const { purposes } = (await state.json()) as { purposes: ConsentPurpose[] };
  const decisions = purposes
    .filter((p) => p.kind === "cookie_category" && !p.necessary)
    .map((p) => ({ purpose: p.id, granted: false, document_version: p.document_version, based_on: p.based_on }));
  const put = await context.request.put("/api/privacy/consent", {
    headers: { Origin: BOARD_ORIGIN },
    data: { source: "banner", decisions },
  });
  if (!put.ok()) throw new Error(`reject all consent: ${put.status()} ${await put.text()}`);
}
