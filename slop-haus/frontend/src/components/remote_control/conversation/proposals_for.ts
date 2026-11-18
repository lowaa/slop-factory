import type { Proposal } from "@/api";

// The pending proposals that belong in a session's chat: the remote control's
// own (a proposal names the agent that made it, and these names are unique
// across the fleet).
export function proposalsForConversation(proposals: Proposal[], conductorName: string): Proposal[] {
  return proposals.filter((p) => p.agent_name === conductorName);
}
