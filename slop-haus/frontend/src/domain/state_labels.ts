import type { ConversationDetail } from "@/api";

const OFFLINE_LABEL = "Offline";

// A session is connected while it has a harness process (Claude Code or pi) on its machine.
export const STATE_LABELS: Record<ConversationDetail["state"], string> = {
  starting: "Connecting",
  ready: "Connected",
  failed: "Failed",
  stopped: "Disconnected",
};

// Live is the one session of a machine that has a process, which another
// session's start or reconnect ends.
export function isLiveState(state: ConversationDetail["state"]): boolean {
  return state === "starting" || state === "ready";
}

// A live session on a machine known to be offline has no process to talk to:
// "Connected" would be false. It is not "Disconnected" either, which means the
// session was stopped and its next message reconnects at once; here the
// message waits for the host to come back and resume it. Unknown presence
// (null) keeps the recorded state's label.
export function sessionLabel(state: ConversationDetail["state"], hostOnline: boolean | null): string {
  if (hostOnline === false && isLiveState(state)) return OFFLINE_LABEL;
  return STATE_LABELS[state];
}
