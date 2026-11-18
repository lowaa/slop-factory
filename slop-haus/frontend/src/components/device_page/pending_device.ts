import { parseSlotCap } from "@/domain/slot_cap";
import { registerPersonalState } from "@/lib/personal_state";

// Split from device_page.tsx the same way authorize_page's pending request
// id lives in pending_authorize.ts (see that file's doc comment) --
// module-scope state that survives the one round trip through /login,
// which replaces the URL and would otherwise lose the "?code=" / "?agent="
// query string a workspace's token.sh appended to the verification URL it
// printed.
//
// Unlike the request id in pending_authorize.ts, /device has something
// worth remembering even with no query string at all: a bare visit while
// signed out still has to come back to /device (not the board) once signed
// in, for someone typing a code in by hand rather than following a link.
// pendingVisit is what carries that.

let pendingVisit = false;
let pendingCode: string | null = null;
let pendingAgent: string | null = null;
let pendingKind: string | null = null;
let pendingSlots: string | null = null;

export function hasPendingDeviceVisit(): boolean {
  return pendingVisit;
}

export function clearPendingDevice(): void {
  pendingVisit = false;
  pendingCode = null;
  pendingAgent = null;
  pendingKind = null;
  pendingSlots = null;
}

export interface CapturedDeviceParams {
  code: string | null;
  agent: string | null;
  // supervisor is what "?kind=supervisor&slots=N" asked for, or null for
  // anything else. It is only a request the consent page shows: nothing is
  // granted unless the human approves a choice that says so.
  supervisor: SupervisorHint | null;
}

export interface SupervisorHint {
  slots: number;
}

// supervisorHint accepts only the exact pair slop-haus-supervisor login
// builds. The URL is attacker-controllable, so a partial or malformed pair is
// dropped whole rather than guessed at, and the slot count is held to the
// server's own limit.
function supervisorHint(kind: string | null, slots: string | null): SupervisorHint | null {
  if (kind !== "supervisor" || slots === null) return null;
  const n = parseSlotCap(slots);
  return n === null ? null : { slots: n };
}

// captureDeviceParams reads "code", "agent", "kind" and "slots" from the URL's query string
// on the first visit, stashing whichever are present for the return trip --
// a query-less call (the return leg, now on /login, or a re-render of
// /device itself) leaves whatever was already stashed untouched.
export function captureDeviceParams(): CapturedDeviceParams {
  pendingVisit = true;
  const params = new URLSearchParams(window.location.search);
  const code = params.get("code");
  const agent = params.get("agent");
  if (code) pendingCode = code;
  if (agent) pendingAgent = agent;
  const kind = params.get("kind");
  const slots = params.get("slots");
  // The pair is taken from one URL or not at all: a later link carrying only
  // one of them must not complete itself from an earlier link's other half.
  if (kind || slots) {
    pendingKind = kind;
    pendingSlots = slots;
  }
  return { code: pendingCode, agent: pendingAgent, supervisor: supervisorHint(pendingKind, pendingSlots) };
}

registerPersonalState("pending device", { forget: clearPendingDevice });
