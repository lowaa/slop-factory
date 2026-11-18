import type { JsonBodyType } from "msw";
import { http, HttpResponse } from "msw/http";
import { createFetchConsentApi } from "../consent_api";
import { ConsentStore, type ConsentStoreOptions } from "../consent_store";

// Absolute because Node's fetch (what jsdom tests run on) has no base URL.
export const BASE = "http://consent.test/api/privacy/consent";

// A purpose as the server's consent route sends it.
export interface WirePurpose {
  id: string;
  kind: "cookie_category" | "processing";
  necessary: boolean;
  granted: boolean;
  needs_decision: boolean;
  gpc_locked: boolean;
  document_kind?: string;
  document_version?: string;
  based_on: number;
}

export function wirePurpose(id: string, over: Partial<WirePurpose> = {}): WirePurpose {
  return {
    id,
    kind: "cookie_category",
    necessary: id === "necessary",
    granted: id === "necessary",
    needs_decision: id !== "necessary",
    gpc_locked: false,
    document_kind: "cookie_policy",
    document_version: "2026-10-01",
    based_on: 0,
    ...over,
  };
}

export function wireState(purposes: WirePurpose[], over: { gpc?: boolean; signed_in?: boolean } = {}) {
  return {
    purposes,
    needs_decision: purposes.filter((p) => p.needs_decision).map((p) => p.id),
    gpc: over.gpc ?? false,
    signed_in: over.signed_in ?? false,
  };
}

// Nothing decided yet: the first visit.
export function freshState() {
  return wireState([wirePurpose("necessary"), wirePurpose("analytics"), wirePurpose("marketing", { })]);
}

export function decidedState(grants: Record<string, boolean>, over: { gpc?: boolean } = {}) {
  return wireState(
    [
      wirePurpose("necessary"),
      ...Object.entries(grants).map(([id, granted]) =>
        wirePurpose(id, { granted, needs_decision: false, based_on: 1 }),
      ),
    ],
    over,
  );
}

export interface PutBody {
  source: string;
  decisions: { purpose: string; granted: boolean; document_version: string; based_on: number }[];
}

// What the test's "server" is doing right now. The handlers answer with
// whatever `state` is and record what they were sent; they decide nothing
// themselves, so each test says what the server would have answered.
export class ServerStub {
  state: JsonBodyType = freshState();
  // What the next PUT answers; the current state if unset.
  nextPut: (() => Response) | null = null;
  gets = 0;
  puts: PutBody[] = [];

  readonly handlers = [
    http.get(BASE, () => {
      this.gets++;
      return HttpResponse.json(this.state);
    }),
    http.put(BASE, async ({ request }) => {
      this.puts.push((await request.json()) as PutBody);
      return this.nextPut ? this.nextPut() : HttpResponse.json(this.state);
    }),
  ];
}

export function newStore(options: ConsentStoreOptions & { fetch?: typeof fetch } = {}): ConsentStore {
  const { fetch: fetchFn, ...rest } = options;
  return new ConsentStore({
    api: createFetchConsentApi({ basePath: BASE, fetch: fetchFn }),
    // A test has no other tab to coordinate with.
    locks: null,
    createChannel: null,
    ...rest,
  });
}
