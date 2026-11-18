import { useMemo, type ReactNode } from "react";
import { useAgents } from "@/lib/shared_resources";
import { AgentNamesContext, useAgentName } from "./agent_names";

// Agents are referred to by id everywhere on the wire -- comment authors,
// the agent a discussion is waiting on, handoffs -- and a
// uuid means nothing to the human reading the board. One lookup for the
// whole app rather than a fetch per component: the fleet is a handful of
// agents, so the full list is cheap and every view shares it.
// Archived agents are in it: archiving never changes how an agent is named.

// Often enough that an agent registered while the page is open gets its
// name within a minute; the list is small, so there is nothing to save by
// being cleverer about when to fetch it.
const REFRESH_MS = 60_000;

export function AgentNamesProvider({ children }: { children: ReactNode }) {
  // A failed fetch keeps whatever names were already known (the cache keeps
  // the last good data); the fallback covers the rest until the next refresh.
  const { data } = useAgents({ archived: true }, { pollMs: REFRESH_MS });
  const names = useMemo(() => Object.fromEntries((data ?? []).map((a) => [a.id, a.name])), [data]);

  return <AgentNamesContext.Provider value={names}>{children}</AgentNamesContext.Provider>;
}

// AgentName renders an agent by name, keeping the full id on hover for
// when the human needs to match it against something else.
export default function AgentName({ id }: { id: string }) {
  const name = useAgentName(id);
  return <span title={id}>{name}</span>;
}
