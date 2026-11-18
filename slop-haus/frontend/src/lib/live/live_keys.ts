import type { ResourceKey } from "@/lib/resource";

// The resources that follow the board stream. Kept beside it, rather than in
// resourceKeys, so the stream's consumers and their keys change together.
export const liveKeys = {
  proposalsPending: () => ["proposals", "pending"] as const,
  agentMessages: () => ["agent_messages"] as const,
  supervisorStatus: () => ["supervisor_status"] as const,
} satisfies Record<string, (...args: never[]) => ResourceKey>;
